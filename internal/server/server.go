package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"gatelet/internal/protocol"
)

const (
	maxHandshakeBytes = 4096
	streamTimeout     = 5 * time.Minute
)

var handshakeTimeout = 10 * time.Second

type Config struct {
	Domain            string
	Token             string
	Tokens            []Token
	ControlAddr       string
	Logger            *slog.Logger
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
}

type Token struct {
	ID     string
	Value  string
	Active bool
}

type Server struct {
	domain            string
	tokens            map[string]Token
	logger            *slog.Logger
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration

	mu       sync.RWMutex
	sessions map[string]*tunnelSession
}

type tunnelSession struct {
	session *yamux.Session
	remote  string
}

func New(config Config) *Server {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		domain:            normalizeDNSName(config.Domain),
		tokens:            normalizeTokens(config),
		logger:            logger,
		heartbeatInterval: config.HeartbeatInterval,
		heartbeatTimeout:  config.HeartbeatTimeout,
		sessions:          make(map[string]*tunnelSession),
	}
}

func normalizeTokens(config Config) map[string]Token {
	tokens := make(map[string]Token)
	if config.Token != "" {
		tokens[protocol.DefaultTokenID] = Token{
			ID:     protocol.DefaultTokenID,
			Value:  config.Token,
			Active: true,
		}
	}
	for _, token := range config.Tokens {
		if token.ID == "" || token.Value == "" {
			continue
		}
		tokens[token.ID] = token
	}
	return tokens
}

func (s *Server) token(id string) (Token, bool) {
	token, ok := s.tokens[id]
	if !ok || !token.Active {
		return Token{}, false
	}
	return token, true
}

func (s *Server) ServeControl(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		s.logger.Info("control connection accepted", "remote", conn.RemoteAddr().String())
		go s.handleControlConn(conn)
	}
}

func (s *Server) HasTunnel(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tunnel := s.sessions[name]
	return tunnel != nil && !tunnel.session.IsClosed()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	name, ok := s.nameFromHost(r.Host)
	if !ok {
		s.logger.Warn("request host did not match tunnel domain", "host", r.Host, "method", r.Method, "uri", r.URL.RequestURI(), "remote", r.RemoteAddr)
		http.NotFound(w, r)
		return
	}
	s.logger.Info("request received", "name", name, "host", r.Host, "method", r.Method, "uri", r.URL.RequestURI(), "remote", r.RemoteAddr)

	session := s.session(name)
	if session == nil {
		s.logger.Warn("tunnel not found", "name", name, "host", r.Host, "method", r.Method, "uri", r.URL.RequestURI())
		http.NotFound(w, r)
		return
	}

	stream, err := session.OpenStream()
	if err != nil {
		s.logger.Error("open tunnel stream failed", "name", name, "host", r.Host, "method", r.Method, "uri", r.URL.RequestURI(), "error", err)
		http.Error(w, "tunnel unavailable", http.StatusBadGateway)
		return
	}
	defer stream.Close()
	if err := stream.SetDeadline(time.Now().Add(streamTimeout)); err != nil {
		s.logger.Warn("set tunnel stream deadline failed", "name", name, "method", r.Method, "uri", r.URL.RequestURI(), "error", err)
	}

	s.logger.Info("forward started", "name", name, "method", r.Method, "uri", r.URL.RequestURI())
	addForwardedHeaders(r)
	if err := r.Write(stream); err != nil {
		s.logger.Error("write request to tunnel failed", "name", name, "method", r.Method, "uri", r.URL.RequestURI(), "duration", time.Since(started), "error", err)
		http.Error(w, "tunnel write failed", http.StatusBadGateway)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		s.logger.Error("read response from tunnel failed", "name", name, "method", r.Method, "uri", r.URL.RequestURI(), "duration", time.Since(started), "error", err)
		http.Error(w, "tunnel read failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	bytes, err := io.Copy(w, resp.Body)
	if err != nil {
		s.logger.Error("write response to client failed", "name", name, "method", r.Method, "uri", r.URL.RequestURI(), "status", resp.StatusCode, "duration", time.Since(started), "bytes", bytes, "error", err)
		return
	}
	s.logger.Info("forward completed", "name", name, "method", r.Method, "uri", r.URL.RequestURI(), "status", resp.StatusCode, "duration", time.Since(started), "bytes", bytes)
}

func (s *Server) handleControlConn(conn net.Conn) {
	remote := conn.RemoteAddr().String()
	defer s.logger.Info("control connection closed", "remote", remote)

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		s.logger.Warn("set control handshake deadline failed", "remote", remote, "error", err)
	}

	line, err := protocol.ReadLine(conn, maxHandshakeBytes)
	if err != nil {
		s.logger.Warn("read client hello failed", "remote", remote, "error", err)
		_ = conn.Close()
		return
	}

	hello, err := protocol.ParseClientHello(line)
	if err != nil {
		s.logger.Warn("parse client hello failed", "remote", remote, "error", err)
		var unsupported protocol.UnsupportedProtocolError
		if errors.As(err, &unsupported) {
			_, _ = conn.Write([]byte(protocol.HandshakeUnsupportedProtocol))
		} else {
			_, _ = conn.Write([]byte(protocol.HandshakeErr))
		}
		_ = conn.Close()
		return
	}
	s.logger.Info("client hello received", "name", hello.Name, "remote", remote, "protocol_version", hello.ProtocolVersion, "client_version", hello.ClientVersion)

	nonce, err := protocol.NewNonce()
	if err != nil {
		s.logger.Error("create auth challenge failed", "name", hello.Name, "remote", remote, "error", err)
		_ = conn.Close()
		return
	}
	if err := json.NewEncoder(conn).Encode(protocol.ServerChallenge{Nonce: nonce}); err != nil {
		s.logger.Warn("send auth challenge failed", "name", hello.Name, "remote", remote, "error", err)
		_ = conn.Close()
		return
	}

	line, err = protocol.ReadLine(conn, maxHandshakeBytes)
	if err != nil {
		s.logger.Warn("read auth response failed", "name", hello.Name, "remote", remote, "error", err)
		_ = conn.Close()
		return
	}
	response, err := protocol.ParseClientChallengeResponse(line)
	tokenID := response.EffectiveTokenID()
	token, ok := s.token(tokenID)
	if err != nil || !ok || !protocol.ValidChallengeResponse(hello.Name, nonce, token.Value, response.Response) {
		s.logger.Warn("authentication failed", "name", hello.Name, "remote", remote, "token_id", tokenID, "parse_error", err)
		_, _ = conn.Write([]byte(protocol.HandshakeErr))
		_ = conn.Close()
		return
	}
	s.logger.Info("authentication succeeded", "name", hello.Name, "remote", remote, "token_id", tokenID)
	if _, err := conn.Write([]byte(protocol.HandshakeOK)); err != nil {
		s.logger.Warn("send auth ok failed", "name", hello.Name, "remote", remote, "error", err)
		_ = conn.Close()
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		s.logger.Warn("clear control handshake deadline failed", "name", hello.Name, "remote", remote, "error", err)
	}

	session, err := yamux.Server(conn, yamuxConfig(s.heartbeatInterval, s.heartbeatTimeout))
	if err != nil {
		s.logger.Error("start tunnel session failed", "name", hello.Name, "remote", remote, "error", err)
		_ = conn.Close()
		return
	}

	s.register(hello.Name, session, remote)
	<-session.CloseChan()
	s.unregister(hello.Name, session, remote)
}

func (s *Server) register(name string, session *yamux.Session, remote string) {
	record := &tunnelSession{
		session: session,
		remote:  remote,
	}

	s.mu.Lock()
	old := s.sessions[name]
	s.sessions[name] = record
	s.mu.Unlock()

	if old != nil {
		s.logger.Info("tunnel replaced", "name", name, "url", "https://"+name+"."+s.domain, "old_remote", old.remote, "new_remote", remote, "reason", "name_reconnect")
		_ = old.session.Close()
	} else {
		s.logger.Info("tunnel registered", "name", name, "url", "https://"+name+"."+s.domain, "remote", remote)
	}
}

func (s *Server) unregister(name string, session *yamux.Session, remote string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if current := s.sessions[name]; current != nil && current.session == session {
		delete(s.sessions, name)
		s.logger.Info("tunnel unregistered", "name", name, "url", "https://"+name+"."+s.domain, "remote", remote)
	}
}

func (s *Server) session(name string) *yamux.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tunnel := s.sessions[name]
	if tunnel == nil || tunnel.session.IsClosed() {
		return nil
	}
	return tunnel.session
}

func (s *Server) nameFromHost(host string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = normalizeDNSName(host)

	suffix := "." + s.domain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}

	name := strings.TrimSuffix(host, suffix)
	if strings.Contains(name, ".") {
		return "", false
	}
	if err := protocol.ValidateName(name); err != nil {
		return "", false
	}

	return name, true
}

func normalizeDNSName(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimPrefix(name, ".")
	name = strings.TrimSuffix(name, ".")
	return name
}

func yamuxConfig(heartbeatInterval, heartbeatTimeout time.Duration) *yamux.Config {
	config := yamux.DefaultConfig()
	if heartbeatInterval > 0 {
		config.KeepAliveInterval = heartbeatInterval
	}
	if heartbeatTimeout > 0 {
		config.ConnectionWriteTimeout = heartbeatTimeout
	}
	return config
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func addForwardedHeaders(r *http.Request) {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		r.Header.Set("X-Forwarded-For", host)
	}
	r.Header.Set("X-Forwarded-Host", r.Host)
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	r.Header.Set("X-Forwarded-Proto", proto)
}
