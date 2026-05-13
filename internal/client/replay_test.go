package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurlCommandIncludesMethodURLHeadersAndBody(t *testing.T) {
	command, err := CurlCommand(RequestEvent{
		Method:     http.MethodPost,
		RequestURI: "/api/users?active=1",
		Host:       "alex.tun.aresa.me",
		RequestHeader: http.Header{
			"Content-Type": {"application/json"},
			"User-Agent":   {"curl/8.8.0"},
		},
		RequestPreview: BodyPreview{
			Size:        int64(len(`{"name":"Alex"}`)),
			Text:        `{"name":"Alex"}`,
			ContentType: "application/json",
		},
	}, "https://alex.tun.aresa.me")
	if err != nil {
		t.Fatalf("CurlCommand returned error: %v", err)
	}

	for _, want := range []string{
		"curl",
		"-X POST",
		"'https://alex.tun.aresa.me/api/users?active=1'",
		"-H 'Content-Type: application/json'",
		"-H 'User-Agent: curl/8.8.0'",
		"--data-binary '{\"name\":\"Alex\"}'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("curl command missing %q:\n%s", want, command)
		}
	}
}

func TestReplayRequestForwardsStoredRequestToLocalTarget(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotHost, gotContentType string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.RequestURI()
		gotHost = r.Host
		gotContentType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll returned error: %v", err)
		}
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("replayed"))
	}))
	defer local.Close()

	result, err := ReplayRequest(context.Background(), local.URL, RequestEvent{
		Method:     http.MethodPost,
		RequestURI: "/api/users?active=1",
		Host:       "alex.tun.aresa.me",
		RequestHeader: http.Header{
			"Content-Type": {"application/json"},
		},
		RequestPreview: BodyPreview{
			Size:        int64(len(`{"name":"Alex"}`)),
			Text:        `{"name":"Alex"}`,
			ContentType: "application/json",
		},
	})
	if err != nil {
		t.Fatalf("ReplayRequest returned error: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/users?active=1" {
		t.Fatalf("replayed request = %s %s, want POST /api/users?active=1", gotMethod, gotPath)
	}
	if gotHost != "alex.tun.aresa.me" {
		t.Fatalf("Host = %q, want alex.tun.aresa.me", gotHost)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody != `{"name":"Alex"}` {
		t.Fatalf("body = %q, want JSON body", gotBody)
	}
	if result.StatusCode != http.StatusCreated || result.ResponsePreview.Text != "replayed" {
		t.Fatalf("replay result = status %d body %q, want 201 replayed", result.StatusCode, result.ResponsePreview.Text)
	}
	if result.RemoteAddr != "local replay" {
		t.Fatalf("RemoteAddr = %q, want local replay", result.RemoteAddr)
	}
}
