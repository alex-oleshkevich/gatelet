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

func TestRunReconnectsAfterTransientControlClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer ln.Close()

	const token = "secret-token"
	attempts := make(chan int, 2)
	holdSecond := make(chan struct{})
	go func() {
		for i := 1; i <= 2; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			attempts <- i
			completeTestHandshake(t, conn, token)
			if i == 1 {
				_ = conn.Close()
				continue
			}
			<-holdSecond
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(holdSecond)

	events := make(chan RequestEvent, 8)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Name:                  "alex",
			ServerAddr:            ln.Addr().String(),
			Target:                "127.0.0.1:3000",
			Token:                 token,
			Events:                events,
			ReconnectInitialDelay: 10 * time.Millisecond,
			ReconnectMaxDelay:     10 * time.Millisecond,
		})
	}()

	waitForAttempt(t, attempts, 1)
	waitForLifecycleEvent(t, events, EventTunnelConnected)
	waitForLifecycleEvent(t, events, EventTunnelReconnecting)
	waitForAttempt(t, attempts, 2)
	waitForLifecycleEvent(t, events, EventTunnelConnected)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRunDoesNotReconnectAfterServerRejection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer ln.Close()

	attempts := make(chan int, 2)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		attempts <- 1
		_, _ = protocol.ReadLine(conn, 1024)
		_, _ = conn.Write([]byte(protocol.HandshakeNameInUse))
	}()

	err = Run(context.Background(), Config{
		Name:                  "alex",
		ServerAddr:            ln.Addr().String(),
		Target:                "127.0.0.1:3000",
		Token:                 "secret-token",
		ReconnectInitialDelay: 10 * time.Millisecond,
		ReconnectMaxDelay:     10 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "tunnel name already in use") {
		t.Fatalf("error = %q, want duplicate-name error", err.Error())
	}
	waitForAttempt(t, attempts, 1)
	select {
	case attempt := <-attempts:
		t.Fatalf("unexpected reconnect attempt %d after server rejection", attempt)
	case <-time.After(50 * time.Millisecond):
	}
}

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
		if !strings.Contains(line, fmt.Sprintf(`"protocol_version":%d`, protocol.CurrentProtocolVersion)) {
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

func TestRunSuggestsControlPlaintextForPlainControlListener(t *testing.T) {
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
		_, _ = conn.Write([]byte("not tls\n"))
	}()

	err = Run(context.Background(), Config{
		Name:       "alex",
		ServerAddr: ln.Addr().String(),
		Target:     "127.0.0.1:3000",
		Token:      "secret-token",
		ControlTLS: true,
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "--control-plaintext") {
		t.Fatalf("error = %q, want --control-plaintext hint", err.Error())
	}
}

func TestRunSendsTokenIDInChallengeResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer ln.Close()

	tokenID := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = protocol.ReadLine(conn, 1024)
		_ = json.NewEncoder(conn).Encode(protocol.ServerChallenge{Nonce: "nonce-value"})
		line, err := protocol.ReadLine(conn, 1024)
		if err != nil {
			return
		}
		response, err := protocol.ParseClientChallengeResponse(line)
		if err != nil {
			return
		}
		tokenID <- response.TokenID
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_ = Run(ctx, Config{
		Name:       "alex",
		ServerAddr: ln.Addr().String(),
		Target:     "127.0.0.1:3000",
		Token:      "secret-token",
		TokenID:    "current",
	})

	select {
	case got := <-tokenID:
		if got != "current" {
			t.Fatalf("TokenID = %q, want current", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for challenge response")
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

func TestPublicURLDefaultsDomainFromWebSocketServerURL(t *testing.T) {
	got := PublicURL("alex", "", "wss://tun.aresa.me/__gatelet/control")
	want := "https://alex.tun.aresa.me"
	if got != want {
		t.Fatalf("PublicURL = %q, want %q", got, want)
	}
}

func TestDefaultWebSocketControlPath(t *testing.T) {
	tests := map[string]string{
		"wss://tun.aresa.me":        "wss://tun.aresa.me/__gatelet/control",
		"wss://tun.aresa.me/":       "wss://tun.aresa.me/__gatelet/control",
		"ws://localhost:8080":       "ws://localhost:8080/__gatelet/control",
		"wss://tun.aresa.me/custom": "wss://tun.aresa.me/custom",
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := webSocketControlURL(input)
			if err != nil {
				t.Fatalf("webSocketControlURL returned error: %v", err)
			}
			if got != want {
				t.Fatalf("webSocketControlURL = %q, want %q", got, want)
			}
		})
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
		ErrorKind:   ErrorKindLocalTarget,
	}, LogFormatJSONL)
	if err != nil {
		t.Fatalf("RequestLogLine returned error: %v", err)
	}

	var record struct {
		Status    int    `json:"status"`
		Error     string `json:"error"`
		ErrorKind string `json:"error_kind"`
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
	if record.ErrorKind != string(ErrorKindLocalTarget) {
		t.Fatalf("ErrorKind = %q, want %q", record.ErrorKind, ErrorKindLocalTarget)
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

func TestHandleStreamUsesConfiguredPreviewLimit(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	events := make(chan RequestEvent, 4)
	go handleStream(context.Background(), serverConn, Config{
		Target:       local.URL,
		Events:       events,
		PreviewLimit: 4,
	})

	_, _ = fmt.Fprint(clientConn, "POST /body HTTP/1.1\r\nHost: alex.example.test\r\nContent-Length: 6\r\n\r\nabcdef")
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse returned error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	var completed RequestEvent
	deadline := time.After(time.Second)
	for completed.Type != EventRequestCompleted {
		select {
		case event := <-events:
			completed = event
		case <-deadline:
			t.Fatal("timed out waiting for completed event")
		}
	}
	if completed.RequestPreview.Text != "abcd" {
		t.Fatalf("RequestPreview.Text = %q, want abcd", completed.RequestPreview.Text)
	}
	if !completed.RequestPreview.Omitted || completed.RequestPreview.Reason != "truncated" {
		t.Fatalf("RequestPreview = %+v, want truncated preview", completed.RequestPreview)
	}
}

func TestRunEmitsTunnelConnectedEventAfterHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer ln.Close()

	const token = "secret-token"
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		line, err := protocol.ReadLine(conn, 1024)
		if err != nil {
			return
		}
		hello, err := protocol.ParseClientHello(line)
		if err != nil {
			return
		}
		challenge := protocol.ServerChallenge{Nonce: "nonce-value"}
		if err := json.NewEncoder(conn).Encode(challenge); err != nil {
			return
		}
		line, err = protocol.ReadLine(conn, 1024)
		if err != nil {
			return
		}
		response, err := protocol.ParseClientChallengeResponse(line)
		if err != nil {
			return
		}
		if !protocol.ValidChallengeResponse(hello.Name, challenge.Nonce, token, response.Response) {
			return
		}
		_, _ = io.WriteString(conn, protocol.HandshakeOK)
		<-time.After(100 * time.Millisecond)
	}()

	events := make(chan RequestEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Name:       "alex",
			ServerAddr: ln.Addr().String(),
			Target:     "127.0.0.1:3000",
			Token:      token,
			Events:     events,
		})
	}()

	select {
	case event := <-events:
		if event.Type != EventTunnelConnected {
			t.Fatalf("event.Type = %q, want %q", event.Type, EventTunnelConnected)
		}
	case err := <-done:
		t.Fatalf("Run returned before connected event: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connected event")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Run to stop")
	}
}

func TestHandleStreamReportsForwardedTargetURL(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	events := make(chan RequestEvent, 4)
	go handleStream(context.Background(), serverConn, Config{
		Target: local.URL + "/base",
		Events: events,
	})

	_, _ = fmt.Fprint(clientConn, "GET /hello?name=alex HTTP/1.1\r\nHost: alex.example.test\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse returned error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	var completed RequestEvent
	deadline := time.After(time.Second)
	for completed.Type != EventRequestCompleted {
		select {
		case event := <-events:
			completed = event
		case <-deadline:
			t.Fatal("timed out waiting for completed event")
		}
	}

	want := local.URL + "/base/hello?name=alex"
	if completed.TargetURL != want {
		t.Fatalf("TargetURL = %q, want %q", completed.TargetURL, want)
	}
}

func TestHandleStreamReportsResponseStartedBeforeStreamingCompletes(t *testing.T) {
	release := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
		_, _ = w.Write([]byte("data: done\n\n"))
	}))
	defer local.Close()
	defer close(release)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	events := make(chan RequestEvent, 8)
	go handleStream(context.Background(), serverConn, Config{
		Target: local.URL,
		Events: events,
	})

	_, _ = fmt.Fprint(clientConn, "GET /events HTTP/1.1\r\nHost: alex.example.test\r\n\r\n")

	var started RequestEvent
	deadline := time.After(time.Second)
	for started.Type != EventResponseStarted {
		select {
		case event := <-events:
			started = event
		case <-deadline:
			t.Fatal("timed out waiting for response started event")
		}
	}
	if started.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", started.StatusCode, http.StatusOK)
	}
	if got := started.ResponseHeader.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if started.TargetURL != local.URL+"/events" {
		t.Fatalf("TargetURL = %q, want %q", started.TargetURL, local.URL+"/events")
	}
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

func TestHandleStreamMarksLocalTargetUnavailable(t *testing.T) {
	target := unavailableLocalTarget(t)
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	events := make(chan RequestEvent, 4)
	requestLog := newRequestLogRecorder()
	go handleStream(context.Background(), serverConn, Config{
		Target:     target,
		RequestLog: requestLog,
		LogFormat:  LogFormatJSON,
		Events:     events,
	})

	_, _ = fmt.Fprint(clientConn, "GET /down HTTP/1.1\r\nHost: alex.example.test\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse returned error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}

	var failed RequestEvent
	deadline := time.After(time.Second)
	for failed.Type != EventRequestFailed {
		select {
		case event := <-events:
			failed = event
		case <-deadline:
			t.Fatal("timed out waiting for failed event")
		}
	}
	if failed.ErrorKind != ErrorKindLocalTarget {
		t.Fatalf("ErrorKind = %q, want %q", failed.ErrorKind, ErrorKindLocalTarget)
	}

	var record struct {
		ErrorKind string `json:"error_kind"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(requestLog.String())), &record); err != nil {
		t.Fatalf("request log is not JSON: %v", err)
	}
	if record.ErrorKind != string(ErrorKindLocalTarget) {
		t.Fatalf("logged ErrorKind = %q, want %q", record.ErrorKind, ErrorKindLocalTarget)
	}
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

func unavailableLocalTarget(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	return "http://" + addr
}

func completeTestHandshake(t *testing.T, conn net.Conn, token string) {
	t.Helper()

	line, err := protocol.ReadLine(conn, 1024)
	if err != nil {
		t.Errorf("ReadLine hello returned error: %v", err)
		return
	}
	hello, err := protocol.ParseClientHello(line)
	if err != nil {
		t.Errorf("ParseClientHello returned error: %v", err)
		return
	}
	challenge := protocol.ServerChallenge{Nonce: "nonce-value"}
	if err := json.NewEncoder(conn).Encode(challenge); err != nil {
		t.Errorf("Encode challenge returned error: %v", err)
		return
	}
	line, err = protocol.ReadLine(conn, 1024)
	if err != nil {
		t.Errorf("ReadLine response returned error: %v", err)
		return
	}
	response, err := protocol.ParseClientChallengeResponse(line)
	if err != nil {
		t.Errorf("ParseClientChallengeResponse returned error: %v", err)
		return
	}
	if !protocol.ValidChallengeResponse(hello.Name, challenge.Nonce, token, response.Response) {
		t.Errorf("invalid challenge response for %q", hello.Name)
		return
	}
	if _, err := conn.Write([]byte(protocol.HandshakeOK)); err != nil {
		t.Errorf("Write handshake OK returned error: %v", err)
	}
}

func waitForAttempt(t *testing.T, attempts <-chan int, want int) {
	t.Helper()

	select {
	case got := <-attempts:
		if got != want {
			t.Fatalf("attempt = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for attempt %d", want)
	}
}

func waitForLifecycleEvent(t *testing.T, events <-chan RequestEvent, want EventType) {
	t.Helper()

	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s event", want)
		}
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
