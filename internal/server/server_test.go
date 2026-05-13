package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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
