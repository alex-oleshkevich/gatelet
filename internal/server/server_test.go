package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gatelet/internal/client"
)

func TestServerRoutesSubdomainThroughClientTunnel(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/hello")
		}
		w.Header().Set("X-Gatelet-Test", "ok")
		_, _ = w.Write([]byte("hello from local"))
	}))
	defer local.Close()

	control := listenLocal(t)
	defer control.Close()

	relay := New(Config{
		Domain:      "example.test",
		Token:       "dev-token",
		ControlAddr: control.Addr().String(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		errs <- relay.ServeControl(ctx, control)
	}()

	go func() {
		errs <- client.Run(ctx, client.Config{
			Name:       "alex",
			ServerAddr: control.Addr().String(),
			Target:     local.URL,
			Token:      "dev-token",
		})
	}()

	waitForTunnel(t, relay, "alex")

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/hello", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()

	relay.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got := res.Header.Get("X-Gatelet-Test"); got != "ok" {
		t.Fatalf("X-Gatelet-Test = %q, want %q", got, "ok")
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(body) != "hello from local" {
		t.Fatalf("body = %q, want %q", string(body), "hello from local")
	}
}

func TestServerReturnsLocalRedirectWithoutFollowingIt(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer local.Close()

	relay, cleanup := startTestTunnel(t, local.URL)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/private", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()

	relay.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusFound)
	}
	if got := res.Header.Get("Location"); got != "/login" {
		t.Fatalf("Location = %q, want %q", got, "/login")
	}
}

func TestServerReturnsNotFoundForUnknownTunnel(t *testing.T) {
	var logs bytes.Buffer
	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "http://missing.example.test/", nil)
	req.Host = "missing.example.test"
	rec := httptest.NewRecorder()

	relay.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !strings.Contains(logs.String(), "tunnel not found") {
		t.Fatalf("logs = %q, want tunnel miss log", logs.String())
	}
}

func TestAddForwardedHeadersAddsRemoteIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/", nil)
	req.Host = "alex.example.test"
	req.RemoteAddr = "203.0.113.44:54321"

	addForwardedHeaders(req)

	if got := req.Header.Get("X-Forwarded-For"); got != "203.0.113.44" {
		t.Fatalf("X-Forwarded-For = %q, want remote IP", got)
	}
	if got := req.Header.Get("X-Forwarded-Host"); got != "alex.example.test" {
		t.Fatalf("X-Forwarded-Host = %q, want host", got)
	}
}

func TestClientReportsAuthenticationFailure(t *testing.T) {
	control := listenLocal(t)
	defer control.Close()

	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = relay.ServeControl(ctx, control)
	}()

	err := client.Run(ctx, client.Config{
		Name:       "alex",
		ServerAddr: control.Addr().String(),
		Target:     "127.0.0.1:3000",
		Token:      "wrong-token",
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error = %q, want authentication failure", err.Error())
	}
}

func TestServerMatchesDomainCaseInsensitivelyWithTrailingDots(t *testing.T) {
	relay := New(Config{
		Domain: "Example.Test.",
		Token:  "dev-token",
	})

	name, ok := relay.nameFromHost("Alex.Example.Test.")
	if !ok {
		t.Fatal("nameFromHost rejected mixed-case host with trailing dots")
	}
	if name != "alex" {
		t.Fatalf("name = %q, want %q", name, "alex")
	}
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}

	return ln
}

func startTestTunnel(t *testing.T, target string) (*Server, func()) {
	t.Helper()

	control := listenLocal(t)

	relay := New(Config{
		Domain:      "example.test",
		Token:       "dev-token",
		ControlAddr: control.Addr().String(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = relay.ServeControl(ctx, control)
	}()

	go func() {
		_ = client.Run(ctx, client.Config{
			Name:       "alex",
			ServerAddr: control.Addr().String(),
			Target:     target,
			Token:      "dev-token",
		})
	}()

	waitForTunnel(t, relay, "alex")

	return relay, func() {
		cancel()
		_ = control.Close()
	}
}

func waitForTunnel(t *testing.T, relay *Server, name string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if relay.HasTunnel(name) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for tunnel %q", name)
}
