package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gatelet/internal/protocol"
)

func TestRunDoesNotSendTokenInInitialHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer ln.Close()

	firstLine := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		line, _ := protocol.ReadLine(conn, 256)
		firstLine <- string(line)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = Run(ctx, Config{
		Name:       "alex",
		ServerAddr: ln.Addr().String(),
		Target:     "127.0.0.1:3000",
		Token:      "secret-token",
	})

	select {
	case line := <-firstLine:
		if strings.Contains(line, "secret-token") {
			t.Fatalf("initial handshake leaked token: %q", line)
		}
		if !strings.Contains(line, `"protocol_version":1`) {
			t.Fatalf("initial handshake missing protocol version: %q", line)
		}
		if !strings.Contains(line, `"client_version":`) {
			t.Fatalf("initial handshake missing client version: %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial handshake")
	}
}

func TestRunReportsUnsupportedProtocolResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = protocol.ReadLine(conn, 1024)
		_, _ = conn.Write([]byte(protocol.HandshakeUnsupportedProtocol))
	}()

	err = Run(context.Background(), Config{
		Name:       "alex",
		ServerAddr: ln.Addr().String(),
		Target:     "127.0.0.1:3000",
		Token:      "secret-token",
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("error = %q, want unsupported protocol version", err.Error())
	}
}

func TestLocalHTTPClientDoesNotUseEnvironmentProxy(t *testing.T) {
	transport, ok := localHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("localHTTPClient.Transport = %T, want *http.Transport", localHTTPClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("localHTTPClient uses an environment proxy")
	}
}

func TestTargetRequestURLPreservesBasePathAndQuery(t *testing.T) {
	got, err := targetRequestURL("http://127.0.0.1:3000/base", "/hello?name=alex")
	if err != nil {
		t.Fatalf("targetRequestURL returned error: %v", err)
	}

	want := "http://127.0.0.1:3000/base/hello?name=alex"
	if got != want {
		t.Fatalf("targetRequestURL = %q, want %q", got, want)
	}
}

func TestTargetRequestURLDefaultsToHTTP(t *testing.T) {
	got, err := targetRequestURL("127.0.0.1:3000", "/hello")
	if err != nil {
		t.Fatalf("targetRequestURL returned error: %v", err)
	}

	want := "http://127.0.0.1:3000/hello"
	if got != want {
		t.Fatalf("targetRequestURL = %q, want %q", got, want)
	}
}

func TestTargetRequestURLRejectsUnsupportedSchemes(t *testing.T) {
	if _, err := targetRequestURL("ftp://127.0.0.1:3000", "/hello"); err == nil {
		t.Fatal("targetRequestURL returned nil error")
	}
}

func TestRequestLineIncludesMethodPathAndQuery(t *testing.T) {
	got := RequestLine(http.MethodGet, "/path?query=1")
	want := "GET /path?query=1"
	if got != want {
		t.Fatalf("RequestLine = %q, want %q", got, want)
	}
}

func TestPublicURLDefaultsDomainFromServerAddress(t *testing.T) {
	got := PublicURL("alex", "", "tun.aresa.me:4443")
	want := "https://alex.tun.aresa.me"
	if got != want {
		t.Fatalf("PublicURL = %q, want %q", got, want)
	}
}

func TestRequestLogLineFormatsText(t *testing.T) {
	got, err := RequestLogLine(RequestEvent{
		Method:      http.MethodPost,
		RequestURI:  "/api/items",
		StatusCode:  http.StatusCreated,
		RequestSize: 1536,
		RemoteAddr:  "203.0.113.44:54812",
		Duration:    25 * time.Millisecond,
	}, LogFormatText)
	if err != nil {
		t.Fatalf("RequestLogLine returned error: %v", err)
	}
	want := "POST /api/items 201 1.5kb 203.0.113.44"
	if got != want {
		t.Fatalf("RequestLogLine = %q, want %q", got, want)
	}
}

func TestRequestLogLineFormatsJSON(t *testing.T) {
	got, err := RequestLogLine(RequestEvent{
		Method:      http.MethodGet,
		RequestURI:  "/api/items?limit=1",
		StatusCode:  http.StatusOK,
		RequestSize: 42,
		RemoteAddr:  "203.0.113.44:54812",
		Duration:    25 * time.Millisecond,
	}, LogFormatJSON)
	if err != nil {
		t.Fatalf("RequestLogLine returned error: %v", err)
	}

	var record struct {
		Type        string  `json:"type"`
		Method      string  `json:"method"`
		Path        string  `json:"path"`
		Status      int     `json:"status"`
		RequestSize int64   `json:"request_size"`
		RemoteIP    string  `json:"remote_ip"`
		DurationMS  float64 `json:"duration_ms"`
		Error       string  `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &record); err != nil {
		t.Fatalf("request log is not JSON: %v", err)
	}
	if record.Type != "request" || record.Method != "GET" || record.Path != "/api/items?limit=1" {
		t.Fatalf("unexpected JSON record: %+v", record)
	}
	if record.Status != http.StatusOK || record.RequestSize != 42 || record.RemoteIP != "203.0.113.44" {
		t.Fatalf("unexpected JSON metrics: %+v", record)
	}
	if record.DurationMS != 25 {
		t.Fatalf("DurationMS = %v, want 25", record.DurationMS)
	}
	if record.Error != "" {
		t.Fatalf("Error = %q, want empty", record.Error)
	}
}

func TestRequestLogLineFormatsJSONError(t *testing.T) {
	got, err := RequestLogLine(RequestEvent{
		Method:      http.MethodPost,
		RequestURI:  "/bad",
		RequestSize: 3,
		Duration:    time.Millisecond,
		Error:       "local target unavailable",
	}, LogFormatJSONL)
	if err != nil {
		t.Fatalf("RequestLogLine returned error: %v", err)
	}

	var record struct {
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &record); err != nil {
		t.Fatalf("request log is not JSON: %v", err)
	}
	if record.Status != 0 {
		t.Fatalf("Status = %d, want 0", record.Status)
	}
	if record.Error != "local target unavailable" {
		t.Fatalf("Error = %q, want local target unavailable", record.Error)
	}
}

func TestRequestRemoteAddrPrefersForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.44, 127.0.0.1")

	got := requestRemoteAddr(req)
	want := "203.0.113.44"
	if got != want {
		t.Fatalf("requestRemoteAddr = %q, want %q", got, want)
	}
}

func TestHandleStreamLogsCompletedRequestSummary(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	requestLog := newRequestLogRecorder()
	go handleStream(context.Background(), serverConn, Config{
		Target:     local.URL,
		RequestLog: requestLog,
	})

	_, _ = fmt.Fprint(clientConn, "GET /hello?name=alex HTTP/1.1\r\nHost: alex.example.test\r\nX-Forwarded-For: 203.0.113.44\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse returned error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	requestLog.assert(t, "GET /hello?name=alex 200 0B 203.0.113.44\n")
}

func TestHandleStreamLogsFailedRequestSummary(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	requestLog := newRequestLogRecorder()
	go handleStream(context.Background(), serverConn, Config{
		Target:     "ftp://127.0.0.1:3000",
		RequestLog: requestLog,
	})

	_, _ = fmt.Fprint(clientConn, "POST /bad HTTP/1.1\r\nHost: alex.example.test\r\nContent-Length: 3\r\n\r\nabc")
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse returned error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	requestLog.assert(t, "POST /bad ERR 3B \n")
}

func TestHandleStreamHoldsRequestsWhilePaused(t *testing.T) {
	reachedLocal := make(chan struct{}, 1)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedLocal <- struct{}{}
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	pause := NewPauseController()
	pause.SetPaused(true)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go handleStream(context.Background(), serverConn, Config{
		Target:          local.URL,
		PauseController: pause,
		PauseTimeout:    time.Second,
	})

	_, _ = fmt.Fprint(clientConn, "GET /held HTTP/1.1\r\nHost: alex.example.test\r\n\r\n")
	select {
	case <-reachedLocal:
		t.Fatal("local target received request while tunnel was paused")
	case <-time.After(50 * time.Millisecond):
	}

	pause.SetPaused(false)

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

type requestLogRecorder struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	updated chan struct{}
}

func newRequestLogRecorder() *requestLogRecorder {
	return &requestLogRecorder{updated: make(chan struct{}, 1)}
}

func (r *requestLogRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	n, err := r.buf.Write(p)
	r.mu.Unlock()
	select {
	case r.updated <- struct{}{}:
	default:
	}
	return n, err
}

func (r *requestLogRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func (r *requestLogRecorder) assert(t *testing.T, want string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		if got := r.String(); got == want {
			return
		}
		select {
		case <-r.updated:
		case <-timer.C:
			t.Fatalf("request log = %q, want %q", r.String(), want)
		}
	}
}
