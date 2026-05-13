package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"gatelet/internal/protocol"
)

const maxHandshakeResponseBytes = 1024

var localHTTPClient = newLocalHTTPClient()

type Config struct {
	Name          string
	ServerAddr    string
	Target        string
	Token         string
	Domain        string
	TUI           bool
	ControlTLS    bool
	ClientVersion string

	Events            chan<- RequestEvent
	PauseController   *PauseController
	PauseTimeout      time.Duration
	RequestLog        io.Writer
	LogFormat         LogFormat
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration

	ControlCACertFile         string
	ControlServerName         string
	ControlInsecureSkipVerify bool
	ControlRootCAs            *x509.CertPool
}

var requestID uint64

func Run(ctx context.Context, config Config) error {
	if err := protocol.ValidateName(config.Name); err != nil {
		return err
	}
	if config.PauseTimeout == 0 {
		config.PauseTimeout = DefaultPauseTimeout
	}
	if config.ClientVersion == "" {
		config.ClientVersion = protocol.DefaultClientVersion
	}

	conn, err := dialControl(ctx, config)
	if err != nil {
		return fmt.Errorf("connect server: %w", err)
	}

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	if err := json.NewEncoder(conn).Encode(protocol.ClientHello{
		Name:            config.Name,
		ProtocolVersion: protocol.CurrentProtocolVersion,
		ClientVersion:   config.ClientVersion,
	}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send client hello: %w", err)
	}

	line, err := protocol.ReadLine(conn, maxHandshakeResponseBytes)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("read server challenge: %w", err)
	}
	if strings.HasPrefix(string(line), "ERR ") {
		_ = conn.Close()
		return fmt.Errorf("%s", strings.TrimSpace(string(line)))
	}
	challenge, err := protocol.ParseServerChallenge(line)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("parse server challenge: %w", err)
	}

	if err := json.NewEncoder(conn).Encode(protocol.ClientChallengeResponse{
		Response: protocol.ChallengeResponse(config.Name, challenge.Nonce, config.Token),
	}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send challenge response: %w", err)
	}

	line, err = protocol.ReadLine(conn, maxHandshakeResponseBytes)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("read authentication response: %w", err)
	}
	if string(line) != protocol.HandshakeOK {
		_ = conn.Close()
		return fmt.Errorf("authentication failed")
	}

	session, err := yamux.Client(conn, yamuxConfig(config.HeartbeatInterval, config.HeartbeatTimeout))
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("start tunnel session: %w", err)
	}
	defer session.Close()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			if ctx.Err() != nil || session.IsClosed() {
				return nil
			}
			return fmt.Errorf("accept tunnel stream: %w", err)
		}

		go handleStream(ctx, stream, config)
	}
}

func dialControl(ctx context.Context, config Config) (net.Conn, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", config.ServerAddr)
	if err != nil {
		return nil, err
	}
	if !config.ControlTLS {
		return conn, nil
	}

	tlsConfig, err := controlTLSConfig(config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	tlsConn := tls.Client(conn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("TLS handshake: %w", err)
	}
	return tlsConn, nil
}

func controlTLSConfig(config Config) (*tls.Config, error) {
	rootCAs := config.ControlRootCAs
	if config.ControlCACertFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		data, err := os.ReadFile(config.ControlCACertFile)
		if err != nil {
			return nil, fmt.Errorf("read control CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("control CA file has no certificates")
		}
		rootCAs = pool
	}

	serverName := config.ControlServerName
	if serverName == "" {
		serverName = hostWithoutPort(config.ServerAddr)
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		RootCAs:            rootCAs,
		ServerName:         serverName,
		InsecureSkipVerify: config.ControlInsecureSkipVerify,
	}, nil
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

func handleStream(ctx context.Context, stream net.Conn, config Config) {
	defer stream.Close()

	id := atomic.AddUint64(&requestID, 1)
	started := time.Now()

	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		writeError(stream, http.StatusBadGateway, "bad tunneled request")
		return
	}
	defer req.Body.Close()

	requestURI := req.URL.RequestURI()
	remoteAddr := requestRemoteAddr(req)

	reqBody, reqPreview := wrapBodyForPreview(req.Header, req.Body)
	req.Body = reqBody
	emit(config.Events, RequestEvent{
		ID:            id,
		Type:          EventRequestReceived,
		Time:          started,
		Method:        req.Method,
		RequestURI:    requestURI,
		Host:          req.Host,
		RemoteAddr:    remoteAddr,
		RequestHeader: cloneHeader(req.Header),
	})

	if config.PauseController != nil {
		wasPaused := config.PauseController.IsPaused()
		if wasPaused {
			emit(config.Events, RequestEvent{
				ID:         id,
				Type:       EventRequestQueued,
				Time:       time.Now(),
				Method:     req.Method,
				RequestURI: requestURI,
				Host:       req.Host,
				RemoteAddr: remoteAddr,
				QueueDepth: config.PauseController.QueueDepth() + 1,
			})
		}
		pauseCtx, cancel := context.WithTimeout(ctx, config.PauseTimeout)
		queued, err := config.PauseController.WaitIfPaused(pauseCtx)
		cancel()
		if queued && !wasPaused {
			emit(config.Events, RequestEvent{
				ID:         id,
				Type:       EventRequestQueued,
				Time:       time.Now(),
				Method:     req.Method,
				RequestURI: requestURI,
				Host:       req.Host,
				RemoteAddr: remoteAddr,
				QueueDepth: config.PauseController.QueueDepth(),
			})
		}
		if err != nil {
			writeError(stream, http.StatusGatewayTimeout, "request paused too long")
			requestPreview := reqPreview.Preview()
			requestSize := requestPreview.Size
			if requestSize == 0 && req.ContentLength > 0 {
				requestSize = req.ContentLength
			}
			event := RequestEvent{
				ID:             id,
				Type:           EventRequestFailed,
				Time:           time.Now(),
				Method:         req.Method,
				RequestURI:     requestURI,
				Host:           req.Host,
				RemoteAddr:     remoteAddr,
				RequestPreview: requestPreview,
				RequestSize:    requestSize,
				Duration:       time.Since(started),
				Error:          err.Error(),
			}
			logRequest(config.RequestLog, config.LogFormat, event)
			emit(config.Events, event)
			return
		}
	}

	targetURL, err := targetRequestURL(config.Target, requestURI)
	if err != nil {
		writeError(stream, http.StatusBadGateway, "bad local target")
		event := failedEvent(id, req, reqPreview, nil, started, err)
		logRequest(config.RequestLog, config.LogFormat, event)
		emit(config.Events, event)
		return
	}

	emit(config.Events, RequestEvent{
		ID:         id,
		Type:       EventRequestForwarding,
		Time:       time.Now(),
		Method:     req.Method,
		RequestURI: requestURI,
		Host:       req.Host,
		RemoteAddr: remoteAddr,
	})

	outReq, err := http.NewRequest(req.Method, targetURL, req.Body)
	if err != nil {
		writeError(stream, http.StatusBadGateway, "bad local request")
		event := failedEvent(id, req, reqPreview, nil, started, err)
		logRequest(config.RequestLog, config.LogFormat, event)
		emit(config.Events, event)
		return
	}
	copyHeader(outReq.Header, req.Header)
	outReq.Host = req.Host

	resp, err := localHTTPClient.Do(outReq)
	if err != nil {
		writeError(stream, http.StatusBadGateway, "local target unavailable")
		event := failedEvent(id, req, reqPreview, nil, started, err)
		logRequest(config.RequestLog, config.LogFormat, event)
		emit(config.Events, event)
		return
	}
	defer resp.Body.Close()

	respBody, respPreview := wrapBodyForPreview(resp.Header, resp.Body)
	resp.Body = respBody
	if err := resp.Write(stream); err != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		event := failedEvent(id, req, reqPreview, respPreview, started, err)
		logRequest(config.RequestLog, config.LogFormat, event)
		emit(config.Events, event)
		return
	}
	requestPreview := reqPreview.Preview()
	responsePreview := respPreview.Preview()
	event := RequestEvent{
		ID:              id,
		Type:            EventRequestCompleted,
		Time:            time.Now(),
		Method:          req.Method,
		RequestURI:      requestURI,
		Host:            req.Host,
		RemoteAddr:      remoteAddr,
		RequestHeader:   cloneHeader(req.Header),
		ResponseHeader:  cloneHeader(resp.Header),
		RequestPreview:  requestPreview,
		ResponsePreview: responsePreview,
		StatusCode:      resp.StatusCode,
		RequestSize:     requestPreview.Size,
		ResponseSize:    responsePreview.Size,
		Duration:        time.Since(started),
	}
	logRequest(config.RequestLog, config.LogFormat, event)
	emit(config.Events, event)
}

func writeError(w io.Writer, status int, message string) {
	resp := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(message + "\n")),
		ContentLength: int64(len(message) + 1),
	}
	_ = resp.Write(w)
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cloneHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	copyHeader(dst, src)
	return dst
}

func emit(events chan<- RequestEvent, event RequestEvent) {
	if events == nil {
		return
	}
	select {
	case events <- event:
	default:
	}
}

func logRequest(w io.Writer, format LogFormat, event RequestEvent) {
	if w == nil {
		return
	}
	line, err := RequestLogLine(event, format)
	if err != nil {
		line = LogLine(event)
	}
	_, _ = fmt.Fprintln(w, line)
}

func failedEvent(id uint64, req *http.Request, reqPreview *bodyPreviewCapture, respPreview *bodyPreviewCapture, started time.Time, err error) RequestEvent {
	requestPreview := reqPreview.Preview()
	requestSize := requestPreview.Size
	if requestSize == 0 && req.ContentLength > 0 {
		requestSize = req.ContentLength
	}
	event := RequestEvent{
		ID:             id,
		Type:           EventRequestFailed,
		Time:           time.Now(),
		Method:         req.Method,
		RequestURI:     req.URL.RequestURI(),
		Host:           req.Host,
		RemoteAddr:     requestRemoteAddr(req),
		RequestHeader:  cloneHeader(req.Header),
		RequestPreview: requestPreview,
		RequestSize:    requestSize,
		Duration:       time.Since(started),
		Error:          err.Error(),
	}
	if respPreview != nil {
		responsePreview := respPreview.Preview()
		event.ResponsePreview = responsePreview
		event.ResponseSize = responsePreview.Size
	}
	return event
}

func requestRemoteAddr(req *http.Request) string {
	forwardedFor := req.Header.Get("X-Forwarded-For")
	if forwardedFor != "" {
		first, _, _ := strings.Cut(forwardedFor, ",")
		return strings.TrimSpace(first)
	}
	return req.RemoteAddr
}

func targetRequestURL(target, requestURI string) (string, error) {
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}

	base, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("unsupported target scheme %q", base.Scheme)
	}
	if base.Host == "" {
		return "", fmt.Errorf("target host is required")
	}

	requestURL, err := url.Parse(requestURI)
	if err != nil {
		return "", err
	}

	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(requestURL.Path, "/")
	base.RawQuery = requestURL.RawQuery
	base.Fragment = ""

	return base.String(), nil
}

func newLocalHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
