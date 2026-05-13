package client

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial handshake")
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

func TestHandleStreamLogsIncomingRequestLine(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	var requestLog bytes.Buffer
	go handleStream(context.Background(), serverConn, Config{
		Target:     local.URL,
		RequestLog: &requestLog,
	})

	_, _ = fmt.Fprint(clientConn, "GET /hello?name=alex HTTP/1.1\r\nHost: alex.example.test\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("ReadResponse returned error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	want := "GET /hello?name=alex\n"
	if requestLog.String() != want {
		t.Fatalf("request log = %q, want %q", requestLog.String(), want)
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
