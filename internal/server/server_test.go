package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gatelet/internal/client"
	"gatelet/internal/protocol"
)

const (
	testAdminUser     = "operator"
	testAdminPassword = "admin-secret"
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

func TestServerRoutesThroughWebSocketControlTunnel(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			t.Fatalf("path = %q, want /hello", r.URL.Path)
		}
		_, _ = w.Write([]byte("hello over websocket"))
	}))
	defer local.Close()

	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
	})
	httpServer := httptest.NewServer(relay)
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		errs <- client.Run(ctx, client.Config{
			Name:       "alex",
			ServerAddr: "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/__gatelet/control",
			Target:     local.URL,
			Token:      "dev-token",
		})
	}()
	waitForTunnel(t, relay, "alex")

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/hello", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "hello over websocket" {
		t.Fatalf("body = %q, want websocket response", got)
	}

	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("client returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
	}
}

func TestServerRoutesThroughSecureWebSocketControlTunnel(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello over secure websocket"))
	}))
	defer local.Close()

	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
	})
	httpServer := httptest.NewTLSServer(relay)
	defer httpServer.Close()

	roots := x509.NewCertPool()
	roots.AddCert(httpServer.Certificate())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		errs <- client.Run(ctx, client.Config{
			Name:           "alex",
			ServerAddr:     "wss" + strings.TrimPrefix(httpServer.URL, "https") + "/__gatelet/control",
			Target:         local.URL,
			Token:          "dev-token",
			ControlRootCAs: roots,
		})
	}()
	waitForTunnel(t, relay, "alex")

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/hello", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "hello over secure websocket" {
		t.Fatalf("body = %q, want secure websocket response", got)
	}

	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("client returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
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

func TestServerTracksTunnelCounters(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll local body returned error: %v", err)
		}
		if string(body) != "hello" {
			t.Fatalf("local body = %q, want hello", string(body))
		}
		_, _ = w.Write([]byte("local response"))
	}))
	defer local.Close()

	var logs lockedBuffer
	relay, cleanup := startTestTunnelWithLogger(t, local.URL, slog.New(slog.NewTextHandler(&logs, nil)))
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "http://alex.example.test/submit", strings.NewReader("hello"))
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	stats, ok := relay.TunnelStats("alex")
	if !ok {
		t.Fatal("TunnelStats returned no stats for alex")
	}
	if stats.Requests != 1 {
		t.Fatalf("Requests = %d, want 1", stats.Requests)
	}
	if stats.BytesIn == 0 {
		t.Fatal("BytesIn = 0, want forwarded request bytes")
	}
	if stats.BytesOut != int64(len("local response")) {
		t.Fatalf("BytesOut = %d, want response body bytes", stats.BytesOut)
	}
	if stats.ConnectedAt.IsZero() {
		t.Fatal("ConnectedAt is zero")
	}
	if stats.LastSeen.Before(stats.ConnectedAt) {
		t.Fatalf("LastSeen = %s before ConnectedAt = %s", stats.LastSeen, stats.ConnectedAt)
	}

	cleanup()
	waitForLog(t, &logs, "tunnel disconnected")
	logText := logs.String()
	for _, want := range []string{"requests=1", "bytes_in=", "bytes_out=", "last_seen=", "disconnect_reason=session_closed"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("disconnect logs missing %q:\n%s", want, logText)
		}
	}
}

func TestServerStatusEndpointReturnsDaemonAndTunnelStats(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))
	defer local.Close()

	relay, cleanup := startTestTunnel(t, local.URL)
	defer cleanup()
	enableAdmin(relay)

	req := httptest.NewRequest(http.MethodPost, "http://alex.example.test/items", strings.NewReader("payload"))
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("forward status = %d, want %d", rec.Code, http.StatusCreated)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "http://example.test/__gatelet/status", nil)
	statusReq.Host = "example.test"
	setAdminAuth(statusReq)
	statusRec := httptest.NewRecorder()
	relay.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status endpoint code = %d, want %d", statusRec.Code, http.StatusOK)
	}
	if got := statusRec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", got)
	}

	var status struct {
		UptimeSeconds int64 `json:"uptime_seconds"`
		ActiveTunnels int   `json:"active_tunnels"`
		Totals        struct {
			Requests uint64 `json:"requests"`
			BytesIn  int64  `json:"bytes_in"`
			BytesOut int64  `json:"bytes_out"`
		} `json:"totals"`
		Tunnels []struct {
			Name         string            `json:"name"`
			Remote       string            `json:"remote"`
			Requests     uint64            `json:"requests"`
			BytesIn      int64             `json:"bytes_in"`
			BytesOut     int64             `json:"bytes_out"`
			StatusCounts map[string]uint64 `json:"status_counts"`
			ConnectedAt  string            `json:"connected_at"`
			LastSeen     string            `json:"last_seen"`
		} `json:"tunnels"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("status JSON invalid: %v", err)
	}
	if status.ActiveTunnels != 1 || len(status.Tunnels) != 1 {
		t.Fatalf("active tunnels = %d len = %d, want one tunnel", status.ActiveTunnels, len(status.Tunnels))
	}
	if status.Totals.Requests != 1 || status.Totals.BytesIn == 0 || status.Totals.BytesOut != int64(len("created")) {
		t.Fatalf("unexpected totals: %+v", status.Totals)
	}
	tunnel := status.Tunnels[0]
	if tunnel.Name != "alex" || tunnel.Requests != 1 || tunnel.StatusCounts["201"] != 1 {
		t.Fatalf("unexpected tunnel status: %+v", tunnel)
	}
	if tunnel.ConnectedAt == "" || tunnel.LastSeen == "" {
		t.Fatalf("missing timestamps: %+v", tunnel)
	}
}

func TestServerMetricsEndpointReturnsPrometheusMetrics(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	}))
	defer local.Close()

	relay, cleanup := startTestTunnel(t, local.URL)
	defer cleanup()
	enableAdmin(relay)

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/metrics-source", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("forward status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "http://example.test/metrics", nil)
	metricsReq.Host = "example.test"
	setAdminAuth(metricsReq)
	metricsRec := httptest.NewRecorder()
	relay.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("metrics endpoint code = %d, want %d", metricsRec.Code, http.StatusOK)
	}

	body := metricsRec.Body.String()
	for _, want := range []string{
		`gatelet_active_tunnels 1`,
		`gatelet_tunnel_requests_total{name="alex",status="202"} 1`,
		`gatelet_tunnel_request_duration_seconds_bucket{name="alex",le=`,
		`gatelet_tunnel_bytes_in_total{name="alex"}`,
		`gatelet_tunnel_bytes_out_total{name="alex"} 8`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestServerAdminEndpointsDoNotOverrideTunnelRoutes(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" && r.URL.Path != "/admin" {
			t.Fatalf("path = %q, want /metrics or /admin", r.URL.Path)
		}
		_, _ = w.Write([]byte("local " + strings.TrimPrefix(r.URL.Path, "/")))
	}))
	defer local.Close()

	relay, cleanup := startTestTunnel(t, local.URL)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/metrics", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "local metrics" {
		t.Fatalf("body = %q, want local metrics", got)
	}

	adminReq := httptest.NewRequest(http.MethodGet, "http://alex.example.test/admin", nil)
	adminReq.Host = "alex.example.test"
	adminRec := httptest.NewRecorder()
	relay.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin tunnel status = %d, want %d", adminRec.Code, http.StatusOK)
	}
	if got := adminRec.Body.String(); got != "local admin" {
		t.Fatalf("body = %q, want local admin", got)
	}
}

func TestServerAdminDashboardRequiresConfiguredCredentials(t *testing.T) {
	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
	req.Host = "example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("admin status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "http://example.test/__gatelet/status", nil)
	statusReq.Host = "example.test"
	statusRec := httptest.NewRecorder()
	relay.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusNotFound {
		t.Fatalf("status endpoint = %d, want %d when admin disabled", statusRec.Code, http.StatusNotFound)
	}
}

func TestServerAdminDashboardRequiresBasicAuth(t *testing.T) {
	relay := New(Config{
		Domain:        "example.test",
		Token:         "dev-token",
		AdminUser:     testAdminUser,
		AdminPassword: testAdminPassword,
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
	req.Host = "example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Gatelet Admin") {
		t.Fatalf("WWW-Authenticate = %q, want Gatelet Admin realm", got)
	}

	wrongReq := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
	wrongReq.Host = "example.test"
	wrongReq.SetBasicAuth(testAdminUser, "wrong")
	wrongRec := httptest.NewRecorder()
	relay.ServeHTTP(wrongRec, wrongReq)
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong auth status = %d, want %d", wrongRec.Code, http.StatusUnauthorized)
	}

	okReq := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
	okReq.Host = "example.test"
	setAdminAuth(okReq)
	okRec := httptest.NewRecorder()
	relay.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("correct auth status = %d, want %d", okRec.Code, http.StatusOK)
	}
	if body := okRec.Body.String(); !strings.Contains(body, "Gatelet relay") || !strings.Contains(body, "/admin/assets/htmx.min.js") {
		t.Fatalf("admin body missing expected dashboard content:\n%s", body)
	}
}

func TestServerAdminDisconnectRequiresCSRFAndClosesTunnel(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	relay, cleanup := startTestTunnel(t, local.URL)
	defer cleanup()
	enableAdmin(relay)

	req := httptest.NewRequest(http.MethodPost, "http://example.test/admin/tunnels/alex/disconnect", nil)
	req.Host = "example.test"
	setAdminAuth(req)
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if !relay.HasTunnel("alex") {
		t.Fatal("tunnel disconnected despite missing CSRF token")
	}

	okReq := httptest.NewRequest(http.MethodPost, "http://example.test/admin/tunnels/alex/disconnect", nil)
	okReq.Host = "example.test"
	okReq.Header.Set("X-Gatelet-Admin-Token", relay.adminActionToken)
	setAdminAuth(okReq)
	okRec := httptest.NewRecorder()
	relay.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("disconnect status = %d, want %d", okRec.Code, http.StatusOK)
	}
	waitForNoTunnel(t, relay, "alex")
	if body := okRec.Body.String(); !strings.Contains(body, "Disconnected tunnel alex") {
		t.Fatalf("disconnect response missing notice:\n%s", body)
	}
}

func TestServerRejectsDuplicateTunnelNameAndKeepsFirstClient(t *testing.T) {
	firstLocal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("first"))
	}))
	defer firstLocal.Close()
	secondLocal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("second"))
	}))
	defer secondLocal.Close()

	var logs lockedBuffer
	control := listenLocal(t)
	defer control.Close()
	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = relay.ServeControl(ctx, control)
	}()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.Run(ctx, client.Config{
			Name:       "alex",
			ServerAddr: control.Addr().String(),
			Target:     firstLocal.URL,
			Token:      "dev-token",
		})
	}()
	waitForTunnel(t, relay, "alex")

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- client.Run(ctx, client.Config{
			Name:       "alex",
			ServerAddr: control.Addr().String(),
			Target:     secondLocal.URL,
			Token:      "dev-token",
		})
	}()
	waitForLog(t, &logs, "duplicate tunnel name rejected")

	select {
	case err := <-secondDone:
		if err == nil {
			t.Fatal("second client returned nil error")
		}
		if !strings.Contains(err.Error(), "tunnel name already in use") {
			t.Fatalf("second client error = %q, want duplicate-name error", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second client did not return duplicate-name error")
	}

	select {
	case err := <-firstDone:
		t.Fatalf("first client disconnected after duplicate registration: %v", err)
	default:
	}

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(body) != "first" {
		t.Fatalf("body = %q, want first client", string(body))
	}

	logText := logs.String()
	for _, want := range []string{"duplicate tunnel name rejected", "name=alex", "active_remote=", "duplicate_remote="} {
		if !strings.Contains(logText, want) {
			t.Fatalf("logs missing %q:\n%s", want, logText)
		}
	}
}

func TestServerRejectsDuplicateTunnelNameOverWebSocket(t *testing.T) {
	firstLocal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("first"))
	}))
	defer firstLocal.Close()
	secondLocal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("second"))
	}))
	defer secondLocal.Close()

	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
	})
	httpServer := httptest.NewServer(relay)
	defer httpServer.Close()
	controlURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/__gatelet/control"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.Run(ctx, client.Config{
			Name:       "alex",
			ServerAddr: controlURL,
			Target:     firstLocal.URL,
			Token:      "dev-token",
		})
	}()
	waitForTunnel(t, relay, "alex")

	err := client.Run(ctx, client.Config{
		Name:       "alex",
		ServerAddr: controlURL,
		Target:     secondLocal.URL,
		Token:      "dev-token",
	})
	if err == nil {
		t.Fatal("second client returned nil error")
	}
	if !strings.Contains(err.Error(), "tunnel name already in use") {
		t.Fatalf("second client error = %q, want duplicate-name error", err.Error())
	}

	select {
	case err := <-firstDone:
		t.Fatalf("first client disconnected after duplicate registration: %v", err)
	default:
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
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	req.Header.Set("X-Forwarded-Host", "spoofed.example.test")
	req.Header.Set("X-Forwarded-Proto", "https")

	addForwardedHeaders(req)

	if got := req.Header.Get("X-Forwarded-For"); got != "203.0.113.44" {
		t.Fatalf("X-Forwarded-For = %q, want direct remote IP", got)
	}
	if got := req.Header.Get("X-Forwarded-Host"); got != "alex.example.test" {
		t.Fatalf("X-Forwarded-Host = %q, want host", got)
	}
	if got := req.Header.Get("X-Forwarded-Proto"); got != "http" {
		t.Fatalf("X-Forwarded-Proto = %q, want direct request proto", got)
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

func TestServerRejectsDefaultReservedTunnelName(t *testing.T) {
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
		Name:       "admin",
		ServerAddr: control.Addr().String(),
		Target:     "127.0.0.1:3000",
		Token:      "dev-token",
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "tunnel name not allowed") {
		t.Fatalf("error = %q, want tunnel name not allowed", err.Error())
	}
}

func TestServerAllowsAllowlistedTunnelName(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	control := listenLocal(t)
	defer control.Close()

	relay := New(Config{
		Domain:     "example.test",
		Token:      "dev-token",
		AllowNames: []string{"alex"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = relay.ServeControl(ctx, control)
	}()
	go func() {
		_ = client.Run(ctx, client.Config{
			Name:       "alex",
			ServerAddr: control.Addr().String(),
			Target:     local.URL,
			Token:      "dev-token",
		})
	}()

	waitForTunnel(t, relay, "alex")
}

func TestServerRejectsNameOutsideAllowlist(t *testing.T) {
	control := listenLocal(t)
	defer control.Close()

	relay := New(Config{
		Domain:     "example.test",
		Token:      "dev-token",
		AllowNames: []string{"alex"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = relay.ServeControl(ctx, control)
	}()

	err := client.Run(ctx, client.Config{
		Name:       "mallory",
		ServerAddr: control.Addr().String(),
		Target:     "127.0.0.1:3000",
		Token:      "dev-token",
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "tunnel name not allowed") {
		t.Fatalf("error = %q, want tunnel name not allowed", err.Error())
	}
}

func TestServerAcceptsActiveTokenIDs(t *testing.T) {
	control := listenLocal(t)
	defer control.Close()

	relay := New(Config{
		Domain: "example.test",
		Tokens: []Token{
			{ID: "current", Value: "new-token", Active: true},
			{ID: "previous", Value: "old-token", Active: true},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = relay.ServeControl(ctx, control)
	}()

	go func() {
		_ = client.Run(ctx, client.Config{
			Name:       "current",
			ServerAddr: control.Addr().String(),
			Target:     "127.0.0.1:3000",
			TokenID:    "current",
			Token:      "new-token",
		})
	}()
	go func() {
		_ = client.Run(ctx, client.Config{
			Name:       "previous",
			ServerAddr: control.Addr().String(),
			Target:     "127.0.0.1:3000",
			TokenID:    "previous",
			Token:      "old-token",
		})
	}()

	waitForTunnel(t, relay, "current")
	waitForTunnel(t, relay, "previous")
}

func TestServerRejectsInactiveTokenIDWithoutLoggingTokenValue(t *testing.T) {
	control := listenLocal(t)
	defer control.Close()

	var logs lockedBuffer
	relay := New(Config{
		Domain: "example.test",
		Tokens: []Token{
			{ID: "inactive", Value: "disabled-token", Active: false},
		},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
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
		TokenID:    "inactive",
		Token:      "disabled-token",
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error = %q, want authentication failure", err.Error())
	}

	logText := logs.String()
	if !strings.Contains(logText, "token_id=inactive") {
		t.Fatalf("logs missing token_id:\n%s", logText)
	}
	if strings.Contains(logText, "disabled-token") {
		t.Fatalf("logs leaked token value:\n%s", logText)
	}
}

func TestServerAcceptsLegacySingleTokenWithoutTokenID(t *testing.T) {
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

	conn := authenticateRawControl(t, control.Addr().String(), "legacy", "dev-token")
	defer conn.Close()
	waitForTunnel(t, relay, "legacy")
}

func TestServerRejectsUnsupportedProtocolVersion(t *testing.T) {
	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
	})
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan struct{})
	go func() {
		relay.handleControlConn(serverConn)
		close(done)
	}()

	_, _ = clientConn.Write([]byte(`{"name":"alex","protocol_version":999,"client_version":"old-client"}` + "\n"))
	line, err := protocol.ReadLine(clientConn, 256)
	if err != nil {
		t.Fatalf("ReadLine returned error: %v", err)
	}
	if string(line) != protocol.HandshakeUnsupportedProtocol {
		t.Fatalf("handshake response = %q, want %q", string(line), protocol.HandshakeUnsupportedProtocol)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not close unsupported protocol connection")
	}
}

func TestControlTLSTrustedCertificateRoutesTunnel(t *testing.T) {
	cert, _ := newTestControlCertificate(t)
	caFile := writeControlCAFile(t, cert)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tls ok"))
	}))
	defer local.Close()

	control := tls.NewListener(listenLocal(t), &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
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

	go func() {
		_ = client.Run(ctx, client.Config{
			Name:              "alex",
			ServerAddr:        control.Addr().String(),
			Target:            local.URL,
			Token:             "dev-token",
			ControlTLS:        true,
			ControlCACertFile: caFile,
		})
	}()
	waitForTunnel(t, relay, "alex")

	req := httptest.NewRequest(http.MethodGet, "http://alex.example.test/", nil)
	req.Host = "alex.example.test"
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(body) != "tls ok" {
		t.Fatalf("body = %q, want tls ok", string(body))
	}
}

func TestControlTLSRejectsUntrustedCertificate(t *testing.T) {
	cert, _ := newTestControlCertificate(t)
	control := tls.NewListener(listenLocal(t), &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
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
		Token:      "dev-token",
		ControlTLS: true,
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "TLS handshake") {
		t.Fatalf("error = %q, want TLS handshake failure", err.Error())
	}
}

func TestControlTLSInsecureSkipVerifyAllowsSelfSignedCertificate(t *testing.T) {
	cert, _ := newTestControlCertificate(t)
	control := tls.NewListener(listenLocal(t), &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
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

	go func() {
		_ = client.Run(ctx, client.Config{
			Name:                      "alex",
			ServerAddr:                control.Addr().String(),
			Target:                    "127.0.0.1:3000",
			Token:                     "dev-token",
			ControlTLS:                true,
			ControlInsecureSkipVerify: true,
		})
	}()
	waitForTunnel(t, relay, "alex")
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

func TestControlHandshakeDeadlineClosesIdleConnection(t *testing.T) {
	oldTimeout := handshakeTimeout
	handshakeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { handshakeTimeout = oldTimeout })

	relay := New(Config{
		Domain: "example.test",
		Token:  "dev-token",
	})
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan struct{})
	go func() {
		relay.handleControlConn(serverConn)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control connection stayed open past handshake timeout")
	}
}

func TestControlHeartbeatTimeoutRemovesDeadTunnel(t *testing.T) {
	control := listenLocal(t)
	defer control.Close()

	relay := New(Config{
		Domain:            "example.test",
		Token:             "dev-token",
		HeartbeatInterval: 20 * time.Millisecond,
		HeartbeatTimeout:  20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = relay.ServeControl(ctx, control)
	}()

	conn := authenticateRawControl(t, control.Addr().String(), "alex", "dev-token")
	defer conn.Close()
	waitForTunnel(t, relay, "alex")
	waitForNoTunnel(t, relay, "alex")
}

func TestControlHeartbeatKeepsResponsiveTunnel(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer local.Close()

	control := listenLocal(t)
	defer control.Close()

	relay := New(Config{
		Domain:            "example.test",
		Token:             "dev-token",
		HeartbeatInterval: 20 * time.Millisecond,
		HeartbeatTimeout:  20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = relay.ServeControl(ctx, control)
	}()

	go func() {
		_ = client.Run(ctx, client.Config{
			Name:              "alex",
			ServerAddr:        control.Addr().String(),
			Target:            local.URL,
			Token:             "dev-token",
			HeartbeatInterval: 20 * time.Millisecond,
			HeartbeatTimeout:  20 * time.Millisecond,
		})
	}()

	waitForTunnel(t, relay, "alex")
	time.Sleep(120 * time.Millisecond)
	if !relay.HasTunnel("alex") {
		t.Fatal("responsive tunnel was removed despite heartbeat replies")
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
	return startTestTunnelWithLogger(t, target, nil)
}

func startTestTunnelWithLogger(t *testing.T, target string, logger *slog.Logger) (*Server, func()) {
	t.Helper()

	control := listenLocal(t)

	relay := New(Config{
		Domain:      "example.test",
		Token:       "dev-token",
		ControlAddr: control.Addr().String(),
		Logger:      logger,
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

func waitForNoTunnel(t *testing.T, relay *Server, name string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !relay.HasTunnel(name) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for tunnel %q to disappear", name)
}

func enableAdmin(relay *Server) {
	relay.adminUser = testAdminUser
	relay.adminPassword = testAdminPassword
}

func setAdminAuth(req *http.Request) {
	req.SetBasicAuth(testAdminUser, testAdminPassword)
}

func waitForLog(t *testing.T, logs interface{ String() string }, needle string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for log %q:\n%s", needle, logs.String())
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func authenticateRawControl(t *testing.T, addr, name, token string) net.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial returned error: %v", err)
	}
	hello := protocol.ClientHello{
		Name:            name,
		ProtocolVersion: protocol.CurrentProtocolVersion,
		ClientVersion:   "raw-test",
	}
	if err := json.NewEncoder(conn).Encode(hello); err != nil {
		conn.Close()
		t.Fatalf("Encode hello returned error: %v", err)
	}
	line, err := protocol.ReadLine(conn, 1024)
	if err != nil {
		conn.Close()
		t.Fatalf("ReadLine challenge returned error: %v", err)
	}
	challenge, err := protocol.ParseServerChallenge(line)
	if err != nil {
		conn.Close()
		t.Fatalf("ParseServerChallenge returned error: %v", err)
	}
	response := protocol.ClientChallengeResponse{
		Response: protocol.ChallengeResponse(name, challenge.Nonce, token),
	}
	if err := json.NewEncoder(conn).Encode(response); err != nil {
		conn.Close()
		t.Fatalf("Encode response returned error: %v", err)
	}
	line, err = protocol.ReadLine(conn, 1024)
	if err != nil {
		conn.Close()
		t.Fatalf("ReadLine auth returned error: %v", err)
	}
	if string(line) != protocol.HandshakeOK {
		conn.Close()
		t.Fatalf("auth response = %q, want %q", string(line), protocol.HandshakeOK)
	}
	return conn
}

func newTestControlCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey returned error: %v", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		t.Fatalf("rand.Int returned error: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate returned error: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair returned error: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("AppendCertsFromPEM returned false")
	}
	return cert, roots
}

func writeControlCAFile(t *testing.T, cert tls.Certificate) string {
	t.Helper()

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	path := filepath.Join(t.TempDir(), "control-ca.pem")
	if err := os.WriteFile(path, caPEM, 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	return path
}
