package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
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

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var handshakeTimeout = 10 * time.Second

type Config struct {
	Domain            string
	Token             string
	Tokens            []Token
	ReservedNames     []string
	AllowNames        []string
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
	reservedNames     map[string]struct{}
	allowNames        map[string]struct{}
	startedAt         time.Time
	logger            *slog.Logger
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration

	mu       sync.RWMutex
	sessions map[string]*tunnelSession
	pending  map[string]string
}

type tunnelSession struct {
	session         *yamux.Session
	remote          string
	requests        uint64
	bytesIn         int64
	bytesOut        int64
	statusCounts    map[int]uint64
	durationBuckets []uint64
	connectedAt     time.Time
	lastSeen        time.Time
}

type TunnelStats struct {
	Name            string
	Remote          string
	Requests        uint64
	BytesIn         int64
	BytesOut        int64
	StatusCounts    map[int]uint64
	DurationBuckets []uint64
	ConnectedAt     time.Time
	LastSeen        time.Time
}

type statusResponse struct {
	UptimeSeconds int64          `json:"uptime_seconds"`
	ActiveTunnels int            `json:"active_tunnels"`
	Totals        statusTotals   `json:"totals"`
	Tunnels       []tunnelStatus `json:"tunnels"`
}

type statusTotals struct {
	Requests uint64 `json:"requests"`
	BytesIn  int64  `json:"bytes_in"`
	BytesOut int64  `json:"bytes_out"`
}

type tunnelStatus struct {
	Name         string         `json:"name"`
	Remote       string         `json:"remote"`
	Requests     uint64         `json:"requests"`
	BytesIn      int64          `json:"bytes_in"`
	BytesOut     int64          `json:"bytes_out"`
	StatusCounts map[int]uint64 `json:"status_counts"`
	ConnectedAt  string         `json:"connected_at"`
	LastSeen     string         `json:"last_seen"`
}

func New(config Config) *Server {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		domain:            normalizeDNSName(config.Domain),
		tokens:            normalizeTokens(config),
		reservedNames:     normalizeReservedNames(config.ReservedNames),
		allowNames:        normalizeNameSet(config.AllowNames),
		startedAt:         time.Now(),
		logger:            logger,
		heartbeatInterval: config.HeartbeatInterval,
		heartbeatTimeout:  config.HeartbeatTimeout,
		sessions:          make(map[string]*tunnelSession),
		pending:           make(map[string]string),
	}
}

func normalizeReservedNames(names []string) map[string]struct{} {
	reserved := normalizeNameSet([]string{"admin", "www", "api", "metrics"})
	for _, name := range names {
		if name != "" {
			reserved[name] = struct{}{}
		}
	}
	return reserved
}

func normalizeNameSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			set[name] = struct{}{}
		}
	}
	return set
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

func (s *Server) nameAllowed(name string) bool {
	if _, reserved := s.reservedNames[name]; reserved {
		return false
	}
	if len(s.allowNames) == 0 {
		return true
	}
	_, allowed := s.allowNames[name]
	return allowed
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

func (s *Server) TunnelStats(name string) (TunnelStats, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tunnel := s.sessions[name]
	if tunnel == nil {
		return TunnelStats{}, false
	}
	return tunnel.stats(name), true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if s.isAdminHost(r.Host) {
		switch r.URL.Path {
		case "/__gatelet/status":
			s.serveStatus(w)
			return
		case "/metrics":
			s.serveMetrics(w)
			return
		}
	}

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
	requestCounter := &countingWriter{writer: stream}
	if err := r.Write(requestCounter); err != nil {
		s.recordRequest(name, http.StatusBadGateway, requestCounter.n, 0, time.Since(started))
		s.logger.Error("write request to tunnel failed", "name", name, "method", r.Method, "uri", r.URL.RequestURI(), "duration", time.Since(started), "error", err)
		http.Error(w, "tunnel write failed", http.StatusBadGateway)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		s.recordRequest(name, http.StatusBadGateway, requestCounter.n, 0, time.Since(started))
		s.logger.Error("read response from tunnel failed", "name", name, "method", r.Method, "uri", r.URL.RequestURI(), "duration", time.Since(started), "error", err)
		http.Error(w, "tunnel read failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	bytes, err := io.Copy(w, resp.Body)
	s.recordRequest(name, resp.StatusCode, requestCounter.n, bytes, time.Since(started))
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
	if !s.nameAllowed(hello.Name) {
		s.logger.Warn("tunnel name denied", "name", hello.Name, "remote", remote)
		_, _ = conn.Write([]byte(protocol.HandshakeNameNotAllowed))
		_ = conn.Close()
		return
	}

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
	if !s.reserveName(hello.Name, remote) {
		_, _ = conn.Write([]byte(protocol.HandshakeNameInUse))
		_ = conn.Close()
		return
	}
	if _, err := conn.Write([]byte(protocol.HandshakeOK)); err != nil {
		s.logger.Warn("send auth ok failed", "name", hello.Name, "remote", remote, "error", err)
		s.releaseReservation(hello.Name, remote)
		_ = conn.Close()
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		s.logger.Warn("clear control handshake deadline failed", "name", hello.Name, "remote", remote, "error", err)
	}

	session, err := yamux.Server(conn, yamuxConfig(s.heartbeatInterval, s.heartbeatTimeout))
	if err != nil {
		s.logger.Error("start tunnel session failed", "name", hello.Name, "remote", remote, "error", err)
		s.releaseReservation(hello.Name, remote)
		_ = conn.Close()
		return
	}

	s.register(hello.Name, session, remote)
	<-session.CloseChan()
	s.unregister(hello.Name, session, remote)
}

func (s *Server) reserveName(name string, remote string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing := s.sessions[name]; existing != nil && !existing.session.IsClosed() {
		s.logger.Warn("duplicate tunnel name rejected", "name", name, "active_remote", existing.remote, "duplicate_remote", remote)
		return false
	}
	if activeRemote, ok := s.pending[name]; ok {
		s.logger.Warn("duplicate tunnel name rejected", "name", name, "active_remote", activeRemote, "duplicate_remote", remote)
		return false
	}

	s.pending[name] = remote
	return true
}

func (s *Server) releaseReservation(name string, remote string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending[name] == remote {
		delete(s.pending, name)
	}
}

func (s *Server) register(name string, session *yamux.Session, remote string) {
	now := time.Now()
	record := &tunnelSession{
		session:         session,
		remote:          remote,
		statusCounts:    make(map[int]uint64),
		durationBuckets: make([]uint64, len(durationBuckets)+1),
		connectedAt:     now,
		lastSeen:        now,
	}

	s.mu.Lock()
	delete(s.pending, name)
	s.sessions[name] = record
	s.mu.Unlock()

	s.logger.Info("tunnel connected", "name", name, "url", "https://"+name+"."+s.domain, "remote", remote, "connected_at", now)
}

func (s *Server) unregister(name string, session *yamux.Session, remote string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if current := s.sessions[name]; current != nil && current.session == session {
		delete(s.sessions, name)
		stats := current.stats(name)
		s.logger.Info(
			"tunnel disconnected",
			"name", name,
			"url", "https://"+name+"."+s.domain,
			"remote", remote,
			"requests", stats.Requests,
			"bytes_in", stats.BytesIn,
			"bytes_out", stats.BytesOut,
			"connected_at", stats.ConnectedAt,
			"last_seen", stats.LastSeen,
			"disconnect_reason", "session_closed",
		)
	}
}

func (s *Server) recordRequest(name string, status int, bytesIn, bytesOut int64, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tunnel := s.sessions[name]
	if tunnel == nil {
		return
	}
	tunnel.requests++
	tunnel.bytesIn += bytesIn
	tunnel.bytesOut += bytesOut
	tunnel.statusCounts[status]++
	seconds := duration.Seconds()
	for i, bucket := range durationBuckets {
		if seconds <= bucket {
			tunnel.durationBuckets[i]++
		}
	}
	tunnel.durationBuckets[len(tunnel.durationBuckets)-1]++
	tunnel.lastSeen = time.Now()
}

func (s *Server) serveStatus(w http.ResponseWriter) {
	stats := s.allTunnelStats()
	response := statusResponse{
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		ActiveTunnels: len(stats),
	}
	for _, tunnel := range stats {
		response.Totals.Requests += tunnel.Requests
		response.Totals.BytesIn += tunnel.BytesIn
		response.Totals.BytesOut += tunnel.BytesOut
		response.Tunnels = append(response.Tunnels, tunnelStatus{
			Name:         tunnel.Name,
			Remote:       tunnel.Remote,
			Requests:     tunnel.Requests,
			BytesIn:      tunnel.BytesIn,
			BytesOut:     tunnel.BytesOut,
			StatusCounts: tunnel.StatusCounts,
			ConnectedAt:  tunnel.ConnectedAt.Format(time.RFC3339Nano),
			LastSeen:     tunnel.LastSeen.Format(time.RFC3339Nano),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("write status response failed", "error", err)
	}
}

func (s *Server) serveMetrics(w http.ResponseWriter) {
	stats := s.allTunnelStats()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	var totalRequests uint64
	var totalBytesIn int64
	var totalBytesOut int64
	for _, tunnel := range stats {
		totalRequests += tunnel.Requests
		totalBytesIn += tunnel.BytesIn
		totalBytesOut += tunnel.BytesOut
	}

	_, _ = fmt.Fprintf(w, "# TYPE gatelet_active_tunnels gauge\n")
	_, _ = fmt.Fprintf(w, "gatelet_active_tunnels %d\n", len(stats))
	_, _ = fmt.Fprintf(w, "# TYPE gatelet_requests_total counter\n")
	_, _ = fmt.Fprintf(w, "gatelet_requests_total %d\n", totalRequests)
	_, _ = fmt.Fprintf(w, "# TYPE gatelet_bytes_in_total counter\n")
	_, _ = fmt.Fprintf(w, "gatelet_bytes_in_total %d\n", totalBytesIn)
	_, _ = fmt.Fprintf(w, "# TYPE gatelet_bytes_out_total counter\n")
	_, _ = fmt.Fprintf(w, "gatelet_bytes_out_total %d\n", totalBytesOut)
	_, _ = fmt.Fprintf(w, "# TYPE gatelet_tunnel_requests_total counter\n")
	_, _ = fmt.Fprintf(w, "# TYPE gatelet_tunnel_request_duration_seconds histogram\n")
	_, _ = fmt.Fprintf(w, "# TYPE gatelet_tunnel_bytes_in_total counter\n")
	_, _ = fmt.Fprintf(w, "# TYPE gatelet_tunnel_bytes_out_total counter\n")

	for _, tunnel := range stats {
		statuses := make([]int, 0, len(tunnel.StatusCounts))
		for status := range tunnel.StatusCounts {
			statuses = append(statuses, status)
		}
		sort.Ints(statuses)
		for _, status := range statuses {
			_, _ = fmt.Fprintf(w, "gatelet_tunnel_requests_total{name=%q,status=%q} %d\n", tunnel.Name, fmt.Sprintf("%d", status), tunnel.StatusCounts[status])
		}
		for i, bucket := range durationBuckets {
			_, _ = fmt.Fprintf(w, "gatelet_tunnel_request_duration_seconds_bucket{name=%q,le=%q} %d\n", tunnel.Name, fmt.Sprintf("%g", bucket), tunnel.DurationBuckets[i])
		}
		_, _ = fmt.Fprintf(w, "gatelet_tunnel_request_duration_seconds_bucket{name=%q,le=%q} %d\n", tunnel.Name, "+Inf", tunnel.DurationBuckets[len(tunnel.DurationBuckets)-1])
		_, _ = fmt.Fprintf(w, "gatelet_tunnel_request_duration_seconds_count{name=%q} %d\n", tunnel.Name, tunnel.Requests)
		_, _ = fmt.Fprintf(w, "gatelet_tunnel_bytes_in_total{name=%q} %d\n", tunnel.Name, tunnel.BytesIn)
		_, _ = fmt.Fprintf(w, "gatelet_tunnel_bytes_out_total{name=%q} %d\n", tunnel.Name, tunnel.BytesOut)
	}
}

func (s *Server) allTunnelStats() []TunnelStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.sessions))
	for name, tunnel := range s.sessions {
		if tunnel != nil && !tunnel.session.IsClosed() {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	stats := make([]TunnelStats, 0, len(names))
	for _, name := range names {
		stats = append(stats, s.sessions[name].stats(name))
	}
	return stats
}

func (s *Server) isAdminHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return normalizeDNSName(host) == s.domain
}

func (t *tunnelSession) stats(name string) TunnelStats {
	statusCounts := make(map[int]uint64, len(t.statusCounts))
	for status, count := range t.statusCounts {
		statusCounts[status] = count
	}
	durationCounts := append([]uint64(nil), t.durationBuckets...)
	return TunnelStats{
		Name:            name,
		Remote:          t.remote,
		Requests:        t.requests,
		BytesIn:         t.bytesIn,
		BytesOut:        t.bytesOut,
		StatusCounts:    statusCounts,
		DurationBuckets: durationCounts,
		ConnectedAt:     t.connectedAt,
		LastSeen:        t.lastSeen,
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

type countingWriter struct {
	writer io.Writer
	n      int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.n += int64(n)
	return n, err
}
