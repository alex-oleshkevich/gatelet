package inspect

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gatelet/internal/client"
)

func TestAPICapabilitiesDescribeAgentSurface(t *testing.T) {
	api := httptest.NewServer(NewServer(Config{
		Name:         "alex",
		PublicURL:    "https://alex.example.test",
		Target:       "http://127.0.0.1:3000",
		Token:        "secret-token",
		PreviewLimit: 0,
	}, NewStore(), client.NewPauseController()).Handler())
	defer api.Close()

	req, err := http.NewRequest(http.MethodGet, api.URL+"/api/capabilities", nil)
	if err != nil {
		t.Fatalf("new capabilities request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/capabilities returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/capabilities status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var capabilities capabilitiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if capabilities.APIVersion != "v1" || capabilities.Name != "alex" || capabilities.PublicURL != "https://alex.example.test" {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if !capabilities.Auth.MutationsRequireToken || capabilities.PreviewLimit != client.DefaultPreviewLimit {
		t.Fatalf("capabilities auth/preview = %+v", capabilities)
	}
	for _, endpoint := range []string{"/api/status", "/api/events", "/api/requests", "/openapi.json"} {
		if !contains(capabilities.Endpoints, endpoint) {
			t.Fatalf("capabilities endpoints = %+v, want %s", capabilities.Endpoints, endpoint)
		}
	}
	for _, action := range []string{"pause", "resume", "replay"} {
		if !contains(capabilities.Actions, action) {
			t.Fatalf("capabilities actions = %+v, want %s", capabilities.Actions, action)
		}
	}
}

func TestAPIReadEndpointsRequireTokenWhenConfigured(t *testing.T) {
	store := NewStore()
	store.Apply(client.RequestEvent{ID: 1, Type: client.EventRequestCompleted, RequestURI: "/secret"})
	api := httptest.NewServer(NewServer(Config{
		Token: "secret-token",
	}, store, client.NewPauseController()).Handler())
	defer api.Close()

	for _, path := range []string{"/api/capabilities", "/api/status", "/api/requests", "/api/requests/1", "/openapi.json"} {
		resp, err := http.Get(api.URL + path)
		if err != nil {
			t.Fatalf("GET %s returned error: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s status = %d, want %d", path, resp.StatusCode, http.StatusUnauthorized)
		}
	}
}

func TestAPIMutationsRequireTokenAndReturnStructuredErrors(t *testing.T) {
	store := NewStore()
	store.Apply(client.RequestEvent{ID: 3, Type: client.EventRequestCompleted, RequestURI: "/bad"})
	api := httptest.NewServer(NewServer(Config{
		Target: "http://127.0.0.1:3000",
		Token:  "secret-token",
		Replay: func(context.Context, string, client.RequestEvent) (client.RequestEvent, error) {
			return client.RequestEvent{}, errors.New("request body preview is incomplete")
		},
	}, store, client.NewPauseController()).Handler())
	defer api.Close()

	resp, err := http.Post(api.URL+"/api/pause", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/pause returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized pause status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	var problem errorResponse
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatalf("decode unauthorized error: %v", err)
	}
	if problem.OK || problem.Error.Code != "unauthorized" {
		t.Fatalf("unauthorized error = %+v", problem)
	}

	req, err := http.NewRequest(http.MethodPost, api.URL+"/api/requests/3/replay", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new replay request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/requests/3/replay returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("replay failure status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatalf("decode replay error: %v", err)
	}
	if problem.OK || problem.Error.Code != "replay_failed" || !strings.Contains(problem.Error.Message, "preview is incomplete") {
		t.Fatalf("replay error = %+v", problem)
	}
}

func TestAPIRequestsSupportAgentFilters(t *testing.T) {
	store := NewStore()
	store.Apply(client.RequestEvent{ID: 1, Type: client.EventRequestCompleted, Method: http.MethodGet, RequestURI: "/ok", StatusCode: http.StatusOK})
	store.Apply(client.RequestEvent{ID: 2, Type: client.EventRequestFailed, Method: http.MethodPost, RequestURI: "/api/fail", StatusCode: http.StatusBadGateway, ErrorKind: client.ErrorKindLocalTarget})
	store.Apply(client.RequestEvent{ID: 3, Type: client.EventRequestCompleted, Method: http.MethodPost, RequestURI: "/api/ok", StatusCode: http.StatusCreated})
	api := httptest.NewServer(NewServer(Config{}, store, client.NewPauseController()).Handler())
	defer api.Close()

	resp, err := http.Get(api.URL + "/api/requests?since_id=1&method=POST&path_contains=/api&limit=1")
	if err != nil {
		t.Fatalf("GET filtered requests returned error: %v", err)
	}
	defer resp.Body.Close()

	var requests []requestResponse
	if err := json.NewDecoder(resp.Body).Decode(&requests); err != nil {
		t.Fatalf("decode filtered requests: %v", err)
	}
	if len(requests) != 1 || requests[0].ID != 2 {
		t.Fatalf("filtered requests = %+v, want request 2 only", requests)
	}

	resp, err = http.Get(api.URL + "/api/requests?status=201")
	if err != nil {
		t.Fatalf("GET status filtered requests returned error: %v", err)
	}
	defer resp.Body.Close()
	requests = nil
	if err := json.NewDecoder(resp.Body).Decode(&requests); err != nil {
		t.Fatalf("decode status filtered requests: %v", err)
	}
	if len(requests) != 1 || requests[0].ID != 3 {
		t.Fatalf("status filtered requests = %+v, want request 3 only", requests)
	}
}

func TestAPIStreamsEventsAndServesOpenAPI(t *testing.T) {
	store := NewStore()
	api := httptest.NewServer(NewServer(Config{}, store, client.NewPauseController()).Handler())
	defer api.Close()

	resp, err := http.Get(api.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET /api/events returned error: %v", err)
	}
	defer resp.Body.Close()

	done := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				done <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
		done <- ""
	}()

	store.Apply(client.RequestEvent{ID: 8, Type: client.EventRequestFailed, Method: http.MethodGet, RequestURI: "/live", StatusCode: http.StatusBadGateway})
	select {
	case data := <-done:
		if !strings.Contains(data, `"id":8`) || !strings.Contains(data, `"request_uri":"/live"`) {
			t.Fatalf("event data = %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE event")
	}

	openAPIResp, err := http.Get(api.URL + "/openapi.json")
	if err != nil {
		t.Fatalf("GET /openapi.json returned error: %v", err)
	}
	defer openAPIResp.Body.Close()
	var document map[string]any
	if err := json.NewDecoder(openAPIResp.Body).Decode(&document); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("openapi version = %#v, want 3.1.0", document["openapi"])
	}
}

func TestListenRejectsNonLoopbackAddress(t *testing.T) {
	ln, err := Listen("0.0.0.0:0")
	if err == nil {
		_ = ln.Close()
		t.Fatal("Listen returned nil error for non-loopback address")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Listen error = %q, want loopback hint", err.Error())
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func TestAPIListsRequestsAndControlsPause(t *testing.T) {
	store := NewStore()
	store.Apply(client.RequestEvent{
		ID:              7,
		Type:            client.EventRequestCompleted,
		Time:            time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC),
		Method:          http.MethodPost,
		RequestURI:      "/api/items?limit=1",
		Host:            "alex.example.test",
		RemoteAddr:      "203.0.113.10",
		RequestHeader:   http.Header{"Content-Type": {"application/json"}},
		ResponseHeader:  http.Header{"X-Trace": {"abc"}},
		RequestPreview:  client.BodyPreview{Text: `{"name":"Alex"}`, ContentType: "application/json", Size: 15, Captured: 15},
		ResponsePreview: client.BodyPreview{Text: `{"ok":true}`, ContentType: "application/json", Size: 11, Captured: 11},
		StatusCode:      http.StatusCreated,
		RequestSize:     15,
		ResponseSize:    11,
		Duration:        25 * time.Millisecond,
	})
	pause := client.NewPauseController()
	api := httptest.NewServer(NewServer(Config{
		PublicURL: "https://alex.example.test",
		Target:    "http://127.0.0.1:3000",
	}, store, pause).Handler())
	defer api.Close()

	resp, err := http.Get(api.URL + "/api/requests")
	if err != nil {
		t.Fatalf("GET /api/requests returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/requests status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var requests []requestResponse
	if err := json.NewDecoder(resp.Body).Decode(&requests); err != nil {
		t.Fatalf("decode requests: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests length = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.ID != 7 || got.Method != http.MethodPost || got.RequestURI != "/api/items?limit=1" || got.StatusCode != http.StatusCreated {
		t.Fatalf("request response = %+v", got)
	}
	if got.RequestPreview.Text != `{"name":"Alex"}` || got.DurationMS != 25 {
		t.Fatalf("request preview/duration = %+v", got)
	}

	resp, err = http.Post(api.URL+"/api/pause", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/pause returned error: %v", err)
	}
	defer resp.Body.Close()
	if !pause.IsPaused() {
		t.Fatal("pause controller is not paused after POST /api/pause")
	}

	var status statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode pause status: %v", err)
	}
	if !status.Paused || status.RequestCount != 1 || status.PublicURL != "https://alex.example.test" {
		t.Fatalf("pause status = %+v", status)
	}

	resp, err = http.Post(api.URL+"/api/resume", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/resume returned error: %v", err)
	}
	defer resp.Body.Close()
	if pause.IsPaused() {
		t.Fatal("pause controller is still paused after POST /api/resume")
	}
}

func TestAPIShowsAndReplaysRequest(t *testing.T) {
	store := NewStore()
	store.Apply(client.RequestEvent{
		ID:             4,
		Type:           client.EventRequestCompleted,
		Method:         http.MethodPost,
		RequestURI:     "/submit",
		RequestPreview: client.BodyPreview{Text: "hello", Size: 5, Captured: 5},
	})
	api := httptest.NewServer(NewServer(Config{
		Target: "http://127.0.0.1:3000",
		Replay: func(ctx context.Context, target string, event client.RequestEvent) (client.RequestEvent, error) {
			if target != "http://127.0.0.1:3000" || event.ID != 4 {
				t.Fatalf("replay target/event = %q/%d", target, event.ID)
			}
			event.ID = 9
			event.Type = client.EventRequestCompleted
			event.StatusCode = http.StatusAccepted
			event.RemoteAddr = "local replay"
			return event, nil
		},
	}, store, client.NewPauseController()).Handler())
	defer api.Close()

	resp, err := http.Get(api.URL + "/api/requests/4")
	if err != nil {
		t.Fatalf("GET /api/requests/4 returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/requests/4 status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var detail requestResponse
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode request detail: %v", err)
	}
	if detail.ID != 4 || detail.RequestURI != "/submit" {
		t.Fatalf("detail = %+v", detail)
	}

	resp, err = http.Post(api.URL+"/api/requests/4/replay", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/requests/4/replay returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/requests/4/replay status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var replayed requestResponse
	if err := json.NewDecoder(resp.Body).Decode(&replayed); err != nil {
		t.Fatalf("decode replay result: %v", err)
	}
	if replayed.ID != 9 || replayed.StatusCode != http.StatusAccepted || replayed.RemoteAddr != "local replay" {
		t.Fatalf("replayed = %+v", replayed)
	}
	if got := store.Count(); got != 2 {
		t.Fatalf("store count = %d, want original request and replay result", got)
	}
}
