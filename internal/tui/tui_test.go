package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"gatelet/internal/client"
)

func TestListViewKeepsHelpBarOnLastLine(t *testing.T) {
	m := model{
		url:     "https://alex.tun.aresa.me",
		status:  "online",
		width:   120,
		height:  12,
		now:     time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC),
		index:   make(map[uint64]int),
		message: "ready",
	}

	view := m.View()
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("View height = %d, want %d", got, m.height)
	}

	last := lastLine(view)
	if !strings.Contains(last, "q quit") {
		t.Fatalf("last line = %q, want sticky help bar", last)
	}
}

func TestInspectorTabSwitchKeepsLinesInsideViewport(t *testing.T) {
	m := detailActionModel()
	m.width = 96
	m.height = 24
	m.requests[0].RequestURI = "/start/3b3802a8-7b02-4129-99bb-6be3ebd08602"
	m.requests[0].TargetURL = "http://localhost:8080/start/3b3802a8-7b02-4129-99bb-6be3ebd08602"
	m.requests[0].RequestHeader["Accept"] = []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"}
	m.requests[0].RequestHeader["Accept-Language"] = []string{"en-US,en;q=0.9,ru-BY;q=0.8,ru;q=0.7,be-BY;q=0.6,be;q=0.5,pl;q=0.4,de;q=0.3,zh-CN;q=0.2"}

	updated, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyTab})
	got := updated.(model)
	view := got.View()
	if height := lipgloss.Height(view); height != got.height {
		t.Fatalf("View height = %d, want %d:\n%s", height, got.height, stripANSI(view))
	}
	for i, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > got.width {
			t.Fatalf("line %d width = %d, want <= %d: %q\n%s", i+1, width, got.width, stripANSI(line), stripANSI(view))
		}
	}
	if count := strings.Count(stripANSI(view), "RESPONSE POST FORMAT"); count != 1 {
		t.Fatalf("footer count = %d, want 1:\n%s", count, stripANSI(view))
	}
}

func TestInspectorTabSwitchRequestsScreenClear(t *testing.T) {
	m := detailActionModel()

	updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyTab})
	if cmd == nil {
		t.Fatal("tab switch returned nil command, want screen clear command")
	}
	got := updated.(model)
	if got.inspectorTab != inspectorTabResponse {
		t.Fatalf("inspectorTab = %v, want response", got.inspectorTab)
	}
}

func TestInspectorTabCyclesBothDirections(t *testing.T) {
	m := detailActionModel()

	updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyTab})
	if cmd == nil {
		t.Fatal("first tab returned nil command, want screen clear command")
	}
	got := updated.(model)
	if got.inspectorTab != inspectorTabResponse {
		t.Fatalf("first tab inspectorTab = %v, want response", got.inspectorTab)
	}

	updated, cmd = got.updateKey(tea.KeyMsg{Type: tea.KeyTab})
	if cmd == nil {
		t.Fatal("second tab returned nil command, want screen clear command")
	}
	got = updated.(model)
	if got.inspectorTab != inspectorTabRequest {
		t.Fatalf("second tab inspectorTab = %v, want request", got.inspectorTab)
	}
}

func TestListViewLeavesBlankLineAfterHeader(t *testing.T) {
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		width:  120,
		height: 12,
		now:    time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC),
		index:  make(map[uint64]int),
	}

	lines := strings.Split(stripANSI(m.View()), "\n")
	if len(lines) < 2 {
		t.Fatalf("view has %d lines, want at least 2", len(lines))
	}
	if strings.TrimSpace(lines[1]) != "" {
		t.Fatalf("line after header = %q, want blank", lines[1])
	}
}

func TestListViewShowsApprovedColumns(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		width:  120,
		height: 16,
		now:    now,
		index:  make(map[uint64]int),
		requests: []requestItem{{
			ID:          1,
			Method:      "POST",
			RequestURI:  "/login",
			RemoteAddr:  "203.0.113.44:54321",
			StatusCode:  422,
			RequestSize: 1400,
			StartedAt:   now.Add(-11 * time.Second),
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"POST", "/login", "422", "1.4kb", "203.0.113.44", "11s"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("View missing %q:\n%s", want, plain)
		}
	}
	for _, notWant := range []string{"Method", "Path", "Status", "Remote IP", "REQUEST HEADERS"} {
		if strings.Contains(plain, notWant) {
			t.Fatalf("list view included %q:\n%s", notWant, plain)
		}
	}
}

func TestOldRequestRowsKeepNormalStyling(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(oldProfile)
	})

	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	row := renderRequestRow(requestItem{
		Method:      "POST",
		RequestURI:  "/login",
		RemoteAddr:  "203.0.113.44:54321",
		StatusCode:  422,
		RequestSize: 1400,
		StartedAt:   now.Add(-45 * time.Minute),
	}, 120, now, false)

	if strings.Contains(row, dimStyle.Render("POST")) {
		t.Fatalf("old request row was dimmed:\n%s", stripANSI(row))
	}
	if !strings.Contains(row, status4xxStyle.Render(padRight("422", 7))) {
		t.Fatalf("old request row lost normal status styling:\n%s", row)
	}
}

func TestHeaderShowsTargetHealth(t *testing.T) {
	m := model{
		url:          "https://alex.tun.aresa.me",
		status:       "online",
		targetHealth: targetHealthDown,
		width:        120,
		height:       12,
		now:          time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC),
		index:        make(map[uint64]int),
	}

	plain := stripANSI(m.View())
	if !strings.Contains(plain, "target DOWN") {
		t.Fatalf("header missing target health:\n%s", plain)
	}
}

func TestHeaderForwardingModeFollowsConnectionStatus(t *testing.T) {
	m := model{
		url:          "https://alex.tun.aresa.me",
		status:       "stopped",
		targetHealth: targetHealthUnknown,
		width:        120,
		height:       12,
		now:          time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC),
		index:        make(map[uint64]int),
	}

	plain := stripANSI(m.View())
	if strings.Contains(plain, "running") || strings.Contains(plain, "accepting") {
		t.Fatalf("stopped header should not show active forwarding mode:\n%s", plain)
	}
	if !strings.Contains(plain, "idle") {
		t.Fatalf("stopped header missing idle forwarding mode:\n%s", plain)
	}

	m.status = "online"
	plain = stripANSI(m.View())
	if !strings.Contains(plain, "accepting") {
		t.Fatalf("online header missing accepting forwarding mode:\n%s", plain)
	}
}

func TestTunnelConnectedEventMarksTUIOnline(t *testing.T) {
	events := make(chan client.RequestEvent)
	m := model{
		ctx:          context.Background(),
		status:       "connecting",
		targetHealth: targetHealthUnknown,
		events:       events,
		index:        make(map[uint64]int),
	}

	got, cmd := m.Update(eventMsg(client.RequestEvent{
		Type:      client.EventTunnelConnected,
		Time:      time.Now(),
		PublicURL: "https://alex.tun.example.com",
	}))
	updated := got.(model)
	if updated.status != "online" {
		t.Fatalf("status = %q, want online", updated.status)
	}
	if len(updated.requests) != 0 {
		t.Fatalf("requests = %d, want 0 for lifecycle event", len(updated.requests))
	}
	if updated.url != "https://alex.tun.example.com" {
		t.Fatalf("url = %q, want public URL", updated.url)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want waitEvent command")
	}
}

func TestTargetHealthUpdatesFromRequestEvents(t *testing.T) {
	m := model{index: make(map[uint64]int)}

	m.applyEvent(client.RequestEvent{
		ID:         10,
		Type:       client.EventResponseStarted,
		StatusCode: 200,
		Time:       time.Now(),
	})
	if m.targetHealth != targetHealthOK {
		t.Fatalf("targetHealth = %q, want %q", m.targetHealth, targetHealthOK)
	}

	m.applyEvent(client.RequestEvent{
		ID:         11,
		Type:       client.EventResponseStarted,
		StatusCode: 503,
		Time:       time.Now(),
	})
	if m.targetHealth != targetHealthDegraded {
		t.Fatalf("targetHealth = %q, want %q", m.targetHealth, targetHealthDegraded)
	}

	m.applyEvent(client.RequestEvent{
		ID:         1,
		Type:       client.EventRequestCompleted,
		StatusCode: 200,
		Time:       time.Now(),
	})
	if m.targetHealth != targetHealthOK {
		t.Fatalf("targetHealth = %q, want %q", m.targetHealth, targetHealthOK)
	}

	m.applyEvent(client.RequestEvent{
		ID:         2,
		Type:       client.EventRequestCompleted,
		StatusCode: 503,
		Time:       time.Now(),
	})
	if m.targetHealth != targetHealthDegraded {
		t.Fatalf("targetHealth = %q, want %q", m.targetHealth, targetHealthDegraded)
	}

	m.applyEvent(client.RequestEvent{
		ID:        3,
		Type:      client.EventRequestFailed,
		Error:     "connect: connection refused",
		ErrorKind: client.ErrorKindLocalTarget,
		Time:      time.Now(),
	})
	if m.targetHealth != targetHealthDown {
		t.Fatalf("targetHealth = %q, want %q", m.targetHealth, targetHealthDown)
	}
}

func TestTargetHealthProbeMapsReachability(t *testing.T) {
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer okServer.Close()

	if got := checkTargetHealth(okServer.URL); got != targetHealthOK {
		t.Fatalf("ok target health = %q, want %q", got, targetHealthOK)
	}

	degradedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer degradedServer.Close()

	if got := checkTargetHealth(degradedServer.URL); got != targetHealthDegraded {
		t.Fatalf("degraded target health = %q, want %q", got, targetHealthDegraded)
	}

	if got := checkTargetHealth("ftp://127.0.0.1:1"); got != targetHealthDown {
		t.Fatalf("unsupported target health = %q, want %q", got, targetHealthDown)
	}
}

func TestTargetProbeUpdatesUnknownButDoesNotOverrideTrafficHealth(t *testing.T) {
	m := model{targetHealth: targetHealthUnknown}

	got, _ := m.Update(targetProbeMsg{health: targetHealthOK})
	updated := got.(model)
	if updated.targetHealth != targetHealthOK {
		t.Fatalf("targetHealth = %q, want %q", updated.targetHealth, targetHealthOK)
	}

	updated.updateTargetHealth(requestItem{
		State:      client.EventRequestCompleted,
		StatusCode: http.StatusServiceUnavailable,
	})
	if updated.targetHealth != targetHealthDegraded {
		t.Fatalf("traffic targetHealth = %q, want %q", updated.targetHealth, targetHealthDegraded)
	}

	got, _ = updated.Update(targetProbeMsg{health: targetHealthOK})
	updated = got.(model)
	if updated.targetHealth != targetHealthDegraded {
		t.Fatalf("probe overrode traffic health: got %q, want %q", updated.targetHealth, targetHealthDegraded)
	}
}

func TestClientDoneErrorMarksDisconnected(t *testing.T) {
	m := model{
		ctx:    context.Background(),
		status: "online",
	}

	got, _ := m.Update(clientDoneMsg{err: errors.New("control session closed")})
	updated := got.(model)
	if updated.status != "disconnected" {
		t.Fatalf("status = %q, want disconnected", updated.status)
	}
	if updated.message != "control session closed" {
		t.Fatalf("message = %q, want control session closed", updated.message)
	}
}

func TestReconnectEventMarksTUIReconnecting(t *testing.T) {
	events := make(chan client.RequestEvent)
	m := model{
		ctx:    context.Background(),
		status: "online",
		events: events,
		index:  make(map[uint64]int),
	}

	got, cmd := m.Update(eventMsg(client.RequestEvent{
		Type:  client.EventTunnelReconnecting,
		Error: "control session closed",
		Time:  time.Now(),
	}))
	updated := got.(model)
	if updated.status != "reconnecting" {
		t.Fatalf("status = %q, want reconnecting", updated.status)
	}
	if !strings.Contains(updated.message, "reconnecting") {
		t.Fatalf("message = %q, want reconnecting detail", updated.message)
	}
	if len(updated.requests) != 0 {
		t.Fatalf("requests = %d, want 0 for lifecycle event", len(updated.requests))
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want waitEvent command")
	}
}

func TestResumeMessageClearsAfterQueuedRequestStartsForwarding(t *testing.T) {
	pause := client.NewPauseController()
	m := model{
		ctx:    context.Background(),
		pause:  pause,
		status: "online",
		index:  make(map[uint64]int),
	}

	updated, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	got := updated.(model)
	if !got.paused {
		t.Fatal("paused = false, want true")
	}

	updated, _ = got.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	got = updated.(model)
	if got.message != "resumed: queued requests are forwarding" {
		t.Fatalf("message = %q, want resume forwarding message", got.message)
	}

	got.applyEvent(client.RequestEvent{
		ID:         1,
		Type:       client.EventRequestForwarding,
		Method:     http.MethodGet,
		RequestURI: "/queued",
		Time:       time.Now(),
	})
	if got.message != "" {
		t.Fatalf("message = %q, want cleared after queue starts forwarding", got.message)
	}
}

func TestSelectedRowHighlightsFullWidth(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:      "https://alex.tun.aresa.me",
		status:   "online",
		width:    120,
		height:   12,
		now:      now,
		index:    make(map[uint64]int),
		selected: 0,
		requests: []requestItem{{
			ID:          1,
			Method:      "POST",
			RequestURI:  "/login",
			RemoteAddr:  "203.0.113.44:54321",
			StatusCode:  422,
			RequestSize: 1400,
			StartedAt:   now.Add(-11 * time.Second),
		}},
	}

	lines := strings.Split(m.View(), "\n")
	if len(lines) < 3 {
		t.Fatalf("view has %d lines, want selected row", len(lines))
	}
	if got := lipgloss.Width(lines[2]); got != m.width {
		t.Fatalf("selected row width = %d, want %d: %q", got, m.width, lines[2])
	}
}

func TestInspectorRequestTabShowsOnlyRequestDetails(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewInspector,
		width:  120,
		height: 24,
		now:    now,
		index:  make(map[uint64]int),
		requests: []requestItem{{
			ID:         1,
			Method:     "GET",
			RequestURI: "/api/users?active=1",
			TargetURL:  "http://127.0.0.1:9090/api/users?active=1",
			Host:       "alex.tun.aresa.me",
			StatusCode: 200,
			State:      client.EventRequestCompleted,
			StartedAt:  now.Add(-5 * time.Second),
			RequestHeader: map[string][]string{
				"User-Agent": {"curl/8.7.1"},
			},
			ResponseHeader: map[string][]string{
				"Server": {"upstream"},
			},
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"request inspector", "REQUEST", "URL: GET /api/users?active=1", "Forwarded to", "http://127.0.0.1:9090/api/users?active=1", "Client", "REQUEST HEADERS", "User-Agent: curl/8.7.1", "l response"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("request inspector missing %q:\n%s", want, plain)
		}
	}
	for _, notWant := range []string{"RESPONSE HEADERS", "Server: upstream", "RESPONSE BODY"} {
		if strings.Contains(plain, notWant) {
			t.Fatalf("request inspector included response detail %q:\n%s", notWant, plain)
		}
	}
}

func TestInspectorResponseTabShowsOnlyResponseDetails(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:          "https://alex.tun.aresa.me",
		status:       "online",
		mode:         viewInspector,
		inspectorTab: inspectorTabResponse,
		width:        120,
		height:       24,
		now:          now,
		index:        make(map[uint64]int),
		requests: []requestItem{{
			ID:           1,
			Method:       "GET",
			RequestURI:   "/api/users?active=1",
			TargetURL:    "http://127.0.0.1:9090/api/users?active=1",
			Host:         "alex.tun.aresa.me",
			StatusCode:   200,
			ResponseSize: 12,
			Duration:     7 * time.Millisecond,
			State:        client.EventRequestCompleted,
			StartedAt:    now.Add(-5 * time.Second),
			RequestHeader: map[string][]string{
				"User-Agent": {"curl/8.7.1"},
			},
			ResponseHeader: map[string][]string{
				"Content-Type": {"application/json"},
			},
			ResponsePreview: client.BodyPreview{
				Size:        int64(len(`{"ok":true}`)),
				Captured:    int64(len(`{"ok":true}`)),
				Text:        `{"ok":true}`,
				ContentType: "application/json",
			},
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"response inspector", "RESPONSE", "URL: GET /api/users?active=1", "Target", "http://127.0.0.1:9090/api/users?active=1", "Upstream 7ms", "RESPONSE HEADERS", "Content-Type: application/json", `"ok": true`, "h request"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("response inspector missing %q:\n%s", want, plain)
		}
	}
	for _, notWant := range []string{"REQUEST HEADERS", "User-Agent: curl/8.7.1", "Forwarded to"} {
		if strings.Contains(plain, notWant) {
			t.Fatalf("response inspector included request detail %q:\n%s", notWant, plain)
		}
	}
}

func TestInspectorHeaderDoesNotDuplicateSelectedRequestLine(t *testing.T) {
	m := detailActionModel()
	m.requests[0].Method = "PUT"
	m.requests[0].RequestURI = "/exercise/resource/42"

	lines := strings.Split(stripANSI(m.View()), "\n")
	if len(lines) < 3 {
		t.Fatalf("view too short:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(lines[0], "/exercise/resource/42") {
		t.Fatalf("top header duplicates selected request path: %q", lines[0])
	}
	if got := strings.TrimSpace(lines[2]); got != "REQUEST" {
		t.Fatalf("first body line = %q, want REQUEST", got)
	}
	if got := strings.TrimSpace(lines[3]); got != "URL: PUT /exercise/resource/42" {
		t.Fatalf("url line = %q, want URL: PUT /exercise/resource/42", got)
	}
	for _, line := range lines[3:] {
		if strings.TrimSpace(line) == "PUT /exercise/resource/42" {
			t.Fatalf("request line rendered as duplicate standalone row:\n%s", strings.Join(lines, "\n"))
		}
	}
}

func TestInspectorIdentifiesLocalTargetErrors(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewInspector,
		width:  120,
		height: 20,
		now:    now,
		index:  make(map[uint64]int),
		requests: []requestItem{{
			ID:         1,
			Method:     "GET",
			RequestURI: "/api/users",
			State:      client.EventRequestFailed,
			Error:      "connect: connection refused",
			ErrorKind:  client.ErrorKindLocalTarget,
			StartedAt:  now,
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"Error Kind", "local target", "connect: connection refused"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("inspector missing %q:\n%s", want, plain)
		}
	}
}

func TestInspectorShowsHTTPBasicAuthEnabledWithoutLeakingCredentials(t *testing.T) {
	m := detailActionModel()
	m.httpBasicAuth = true
	m.requests[0].HTTPBasicAuth = true
	m.requests[0].RequestHeader["Authorization"] = []string{"Basic b3BlcmF0b3I6c2VjcmV0"}

	plain := stripANSI(m.View())
	for _, want := range []string{"HTTP Auth: enabled", "Authorization: (redacted)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("inspector missing %q:\n%s", want, plain)
		}
	}
	for _, notWant := range []string{"b3BlcmF0b3I6c2VjcmV0", "operator", "secret"} {
		if strings.Contains(plain, notWant) {
			t.Fatalf("inspector leaked %q:\n%s", notWant, plain)
		}
	}
}

func TestHeaderShowsHTTPBasicAuthState(t *testing.T) {
	m := detailActionModel()
	m.httpBasicAuth = true

	plain := stripANSI(m.renderHeader(120))
	if !strings.Contains(plain, "auth ON") {
		t.Fatalf("header missing auth state:\n%s", plain)
	}
}

func TestInspectorRequestTabFormatsJSONBody(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewInspector,
		width:  120,
		height: 24,
		now:    now,
		index:  make(map[uint64]int),
		requests: []requestItem{{
			ID:         1,
			Method:     "POST",
			RequestURI: "/api/users",
			StatusCode: 201,
			State:      client.EventRequestCompleted,
			StartedAt:  now,
			RequestPreview: client.BodyPreview{
				Size:        int64(len(`{"name":"Alex"}`)),
				Captured:    int64(len(`{"name":"Alex"}`)),
				Text:        `{"name":"Alex"}`,
				ContentType: "application/json",
			},
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{`BODY`, `"name": "Alex"`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("formatted JSON body missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `BODY [formatted json]`) {
		t.Fatalf("formatted JSON label still rendered:\n%s", plain)
	}
	if strings.Contains(plain, `{"name":"Alex"}`) {
		t.Fatalf("formatted JSON body still includes raw compact JSON:\n%s", plain)
	}
}

func TestInspectorFormatsJSONWithoutContentType(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewInspector,
		width:  120,
		height: 24,
		now:    now,
		index:  make(map[uint64]int),
		requests: []requestItem{{
			ID:         1,
			Method:     "POST",
			RequestURI: "/api/users",
			StatusCode: 201,
			State:      client.EventRequestCompleted,
			StartedAt:  now,
			RequestPreview: client.BodyPreview{
				Size:     int64(len(`{"name":"Alex"}`)),
				Captured: int64(len(`{"name":"Alex"}`)),
				Text:     `{"name":"Alex"}`,
			},
		}},
	}

	plain := stripANSI(m.View())
	if !strings.Contains(plain, `"name": "Alex"`) {
		t.Fatalf("JSON without content type was not formatted:\n%s", plain)
	}
}

func TestInspectorTogglesPlainJSONBody(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:       "https://alex.tun.aresa.me",
		status:    "online",
		mode:      viewInspector,
		width:     120,
		height:    24,
		now:       now,
		index:     make(map[uint64]int),
		plainBody: true,
		requests: []requestItem{{
			ID:         1,
			Method:     "POST",
			RequestURI: "/api/users",
			StatusCode: 201,
			State:      client.EventRequestCompleted,
			StartedAt:  now,
			RequestPreview: client.BodyPreview{
				Size:        int64(len(`{"name":"Alex"}`)),
				Captured:    int64(len(`{"name":"Alex"}`)),
				Text:        `{"name":"Alex"}`,
				ContentType: "application/json",
			},
		}},
	}

	plain := stripANSI(m.View())
	if !strings.Contains(plain, `BODY`) || !strings.Contains(plain, `{"name":"Alex"}`) {
		t.Fatalf("plain body missing raw JSON:\n%s", plain)
	}
	if strings.Contains(plain, `BODY [plain]`) {
		t.Fatalf("plain body label still rendered:\n%s", plain)
	}
	if strings.Contains(plain, `"name": "Alex"`) {
		t.Fatalf("plain JSON body was formatted:\n%s", plain)
	}
}

func TestInspectorOpensBodyView(t *testing.T) {
	m := detailActionModel()

	updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if cmd != nil {
		t.Fatal("body view returned command, want immediate update")
	}

	got := updated.(model)
	if got.mode != viewBody {
		t.Fatalf("mode = %v, want viewBody", got.mode)
	}
	if got.bodyScroll != 0 {
		t.Fatalf("bodyScroll = %d, want 0", got.bodyScroll)
	}
}

func TestBodyViewShowsOnlyActiveInspectorTabBody(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:          "https://alex.tun.aresa.me",
		status:       "online",
		mode:         viewBody,
		inspectorTab: inspectorTabResponse,
		width:        120,
		height:       20,
		now:          now,
		index:        make(map[uint64]int),
		requests: []requestItem{{
			ID:         1,
			Method:     "POST",
			RequestURI: "/api/users",
			StatusCode: 201,
			State:      client.EventRequestCompleted,
			StartedAt:  now,
			RequestHeader: map[string][]string{
				"User-Agent": {"curl/8.7.1"},
			},
			RequestPreview: client.BodyPreview{
				Size:        int64(len(`{"name":"Alex"}`)),
				Captured:    int64(len(`{"name":"Alex"}`)),
				Text:        `{"name":"Alex"}`,
				ContentType: "application/json",
			},
			ResponsePreview: client.BodyPreview{
				Size:        int64(len(`{"ok":true}`)),
				Captured:    int64(len(`{"ok":true}`)),
				Text:        `{"ok":true}`,
				ContentType: "application/json",
			},
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"response body", "BODY", `"ok": true`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("body view missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "BODY [formatted json]") {
		t.Fatalf("body view still includes formatted label:\n%s", plain)
	}
	for _, notWant := range []string{"REQUEST HEADERS", "User-Agent: curl/8.7.1", `"name": "Alex"`} {
		if strings.Contains(plain, notWant) {
			t.Fatalf("body view included inactive request data %q:\n%s", notWant, plain)
		}
	}
}

func TestBodyViewAlignsContentLikeInspector(t *testing.T) {
	m := detailActionModel()
	m.width = 100
	m.height = 24

	inspector := strings.Split(stripANSI(m.View()), "\n")
	if len(inspector) < 3 {
		t.Fatalf("inspector view too short:\n%s", strings.Join(inspector, "\n"))
	}
	if strings.TrimSpace(inspector[1]) != "" {
		t.Fatalf("inspector second line = %q, want blank", inspector[1])
	}
	if got := strings.TrimSpace(inspector[2]); got != "REQUEST" {
		t.Fatalf("inspector first body line = %q, want REQUEST", got)
	}
	if got := strings.TrimSpace(inspector[3]); got != "URL: POST /api/users" {
		t.Fatalf("inspector URL line = %q, want URL: POST /api/users", got)
	}

	m.mode = viewBody
	body := strings.Split(stripANSI(m.View()), "\n")
	if len(body) < 3 {
		t.Fatalf("body view too short:\n%s", strings.Join(body, "\n"))
	}
	if strings.TrimSpace(body[1]) != "" {
		t.Fatalf("body second line = %q, want blank", body[1])
	}
	if got := strings.TrimSpace(body[2]); got != "BODY" {
		t.Fatalf("body first body line = %q, want BODY", got)
	}
}

func TestInspectorSwitchesTabsAndReturnsFromBodyToActiveTab(t *testing.T) {
	m := detailActionModel()

	updated, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	got := updated.(model)
	if got.inspectorTab != inspectorTabResponse {
		t.Fatalf("inspectorTab = %v, want response", got.inspectorTab)
	}

	updated, _ = got.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	got = updated.(model)
	if got.mode != viewBody || got.inspectorTab != inspectorTabResponse {
		t.Fatalf("mode/tab = %v/%v, want body/response", got.mode, got.inspectorTab)
	}

	updated, _ = got.updateKey(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.mode != viewInspector || got.inspectorTab != inspectorTabResponse {
		t.Fatalf("mode/tab = %v/%v, want inspector/response", got.mode, got.inspectorTab)
	}
}

func TestInspectorShowsBinaryAndTruncatedBodyMetadata(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:          "https://alex.tun.aresa.me",
		status:       "online",
		mode:         viewInspector,
		inspectorTab: inspectorTabResponse,
		width:        120,
		height:       20,
		now:          now,
		index:        make(map[uint64]int),
		requests: []requestItem{{
			ID:         1,
			Method:     "GET",
			RequestURI: "/download",
			TargetURL:  "http://127.0.0.1:9090/download",
			StatusCode: 200,
			State:      client.EventRequestCompleted,
			StartedAt:  now,
			ResponsePreview: client.BodyPreview{
				ContentType: "application/pdf",
				Size:        10 * 1024 * 1024,
				Captured:    4096,
				Omitted:     true,
				Reason:      "binary body",
			},
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"omitted: binary body", "application/pdf", "captured 4.0kb of 10mb"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("binary metadata missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "%PDF") {
		t.Fatalf("binary body bytes rendered unexpectedly:\n%s", plain)
	}
}

func TestBodyViewTogglesPlainAndScrollsIndependently(t *testing.T) {
	m := detailActionModel()
	m.mode = viewBody
	body := strings.Repeat("line\n", 40)
	m.requests[0].RequestPreview = client.BodyPreview{
		Text:        body,
		Size:        int64(len(body)),
		ContentType: "text/plain",
	}
	m.bodyScroll = 3
	m.detailScroll = 9

	updated, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	got := updated.(model)
	if !got.plainBody {
		t.Fatal("plainBody = false, want true after F")
	}
	if got.bodyScroll != 0 {
		t.Fatalf("bodyScroll = %d, want reset to 0", got.bodyScroll)
	}
	if got.detailScroll != 9 {
		t.Fatalf("detailScroll = %d, want unchanged detail scroll", got.detailScroll)
	}

	updated, _ = got.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	got = updated.(model)
	if got.bodyScroll != 1 {
		t.Fatalf("bodyScroll = %d, want 1 after down", got.bodyScroll)
	}
	if got.detailScroll != 9 {
		t.Fatalf("detailScroll = %d, want unchanged detail scroll", got.detailScroll)
	}
}

func TestBodyViewShowsFullCapturedPreview(t *testing.T) {
	m := detailActionModel()
	m.mode = viewBody
	m.width = 40
	m.height = 12
	body := strings.Repeat("0123456789", 20) + "THE-END"
	m.requests[0].RequestPreview = client.BodyPreview{
		Text:        body,
		Size:        int64(len(body)),
		Captured:    int64(len(body)),
		ContentType: "text/plain",
	}

	plain := stripANSI(formatBodyView(m.requests[0], m.width, true, inspectorTabRequest))
	if !strings.Contains(plain, "THE-END") {
		t.Fatalf("body view content was truncated:\n%s", plain)
	}
	if strings.Contains(plain, "...") {
		t.Fatalf("body view rendered truncation marker:\n%s", plain)
	}

	lines := bodyViewLines(m.requests[0], m.width, true, inspectorTabRequest)
	if len(lines) < 4 {
		t.Fatalf("body view did not wrap long captured content, lines=%d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

func TestInspectorPreviewShowsSmallBodiesAndTruncatesLargeBodies(t *testing.T) {
	m := detailActionModel()
	m.width = 120
	m.height = 24
	m.plainBody = true

	small := strings.Repeat("a", 499)
	m.requests[0].RequestPreview = client.BodyPreview{
		Text:        small,
		Size:        int64(len(small)),
		Captured:    int64(len(small)),
		ContentType: "text/plain",
	}
	plain := stripANSI(formatInspector(m.requests[0], m.width, m.now, m.plainBody, inspectorTabRequest))
	if !strings.Contains(plain, small) {
		t.Fatalf("small body was not fully rendered:\n%s", plain)
	}
	if strings.Contains(plain, "...") {
		t.Fatalf("small body rendered truncation marker:\n%s", plain)
	}

	large := strings.Repeat("b", 501)
	m.requests[0].RequestPreview = client.BodyPreview{
		Text:        large,
		Size:        int64(len(large)),
		Captured:    int64(len(large)),
		ContentType: "text/plain",
	}
	plain = stripANSI(formatInspector(m.requests[0], m.width, m.now, m.plainBody, inspectorTabRequest))
	if strings.Contains(plain, large) {
		t.Fatalf("large body was rendered fully:\n%s", plain)
	}
	if !strings.Contains(plain, strings.Repeat("b", 497)+"...") {
		t.Fatalf("large body was not truncated at 500 chars:\n%s", plain)
	}
}

func TestBodyViewStatusLabelOmitsFormatMode(t *testing.T) {
	m := detailActionModel()
	m.mode = viewBody
	m.plainBody = false

	if got := m.statusLabel(); got != "BODY REQUEST" {
		t.Fatalf("statusLabel = %q, want BODY REQUEST", got)
	}

	m.plainBody = true
	if got := m.statusLabel(); got != "BODY REQUEST" {
		t.Fatalf("plain statusLabel = %q, want BODY REQUEST", got)
	}
}

func TestVisibleRequestsANDsFilterTermsAcrossFields(t *testing.T) {
	m := model{
		filter: "POST /api 500 203.0.113",
		requests: []requestItem{
			{ID: 1, Method: "POST", RequestURI: "/api/users", StatusCode: 500, RemoteAddr: "203.0.113.10:1234"},
			{ID: 2, Method: "POST", RequestURI: "/api/users", StatusCode: 201, RemoteAddr: "203.0.113.10:1234"},
			{ID: 3, Method: "GET", RequestURI: "/api/users", StatusCode: 500, RemoteAddr: "203.0.113.10:1234"},
			{ID: 4, Method: "POST", RequestURI: "/web/users", StatusCode: 500, RemoteAddr: "203.0.113.10:1234"},
			{ID: 5, Method: "POST", RequestURI: "/api/users", StatusCode: 500, RemoteAddr: "198.51.100.1:1234"},
		},
	}

	visible := m.visibleRequests()
	if len(visible) != 1 || visible[0].ID != 1 {
		t.Fatalf("visible = %+v, want only request 1", visible)
	}
}

func TestDetailViewCopiesSelectedRequestAsCurl(t *testing.T) {
	var copied string
	m := detailActionModel()
	m.copyText = func(text string) error {
		copied = text
		return nil
	}

	updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd != nil {
		t.Fatal("copy returned command, want immediate update")
	}
	got := updated.(model)
	if !strings.Contains(copied, "curl -X POST 'https://alex.tun.aresa.me/api/users'") {
		t.Fatalf("copied text = %q, want curl command", copied)
	}
	if !strings.Contains(copied, "--data-binary '{\"name\":\"Alex\"}'") {
		t.Fatalf("copied text missing body: %q", copied)
	}
	if !strings.Contains(got.message, "copied curl") {
		t.Fatalf("message = %q, want copied curl", got.message)
	}
}

func TestDetailViewExportsSelectedRequestAsCurlFile(t *testing.T) {
	m := detailActionModel()
	m.captureDir = t.TempDir()

	updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if cmd != nil {
		t.Fatal("export returned command, want immediate update")
	}
	got := updated.(model)
	if !strings.Contains(got.message, "saved curl") {
		t.Fatalf("message = %q, want saved curl", got.message)
	}

	data, err := os.ReadFile(filepath.Join(m.captureDir, "000001-post-api-users.curl"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !strings.Contains(string(data), "curl -X POST 'https://alex.tun.aresa.me/api/users'") {
		t.Fatalf("saved curl file = %q", string(data))
	}
}

func TestDetailViewReplaysSelectedRequest(t *testing.T) {
	m := detailActionModel()
	m.target = "http://127.0.0.1:3000"
	m.replay = func(ctx context.Context, target string, event client.RequestEvent) (client.RequestEvent, error) {
		if target != "http://127.0.0.1:3000" {
			t.Fatalf("target = %q, want local target", target)
		}
		if event.Method != "POST" || event.RequestURI != "/api/users" {
			t.Fatalf("event = %s %s, want POST /api/users", event.Method, event.RequestURI)
		}
		event.ID = 99
		event.Type = client.EventRequestCompleted
		event.StatusCode = 202
		event.RemoteAddr = "local replay"
		event.Time = m.now
		return event, nil
	}

	updated, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	got := updated.(model)
	if !strings.Contains(got.message, "replaying") {
		t.Fatalf("message = %q, want replaying", got.message)
	}
	if cmd == nil {
		t.Fatal("replay returned nil command")
	}

	msg := cmd()
	updated, _ = got.Update(msg)
	got = updated.(model)
	if len(got.requests) != 2 {
		t.Fatalf("requests = %d, want replay result appended", len(got.requests))
	}
	result := got.requests[0]
	if result.ID != 99 || result.StatusCode != 202 || result.RemoteAddr != "local replay" {
		t.Fatalf("replay result = %+v", result)
	}
	selected, ok := got.selectedRequest()
	if !ok {
		t.Fatal("selectedRequest returned false")
	}
	if selected.ID != 1 {
		t.Fatalf("selected request ID = %d, want original request ID 1", selected.ID)
	}
	if !strings.Contains(got.message, "replay 202") {
		t.Fatalf("message = %q, want replay status", got.message)
	}
}

func TestColorizeJSONKeepsPlainText(t *testing.T) {
	got := stripANSI(colorizeJSON(`{"ok":true,"n":12,"name":"Alex"}`))
	if got != `{"ok":true,"n":12,"name":"Alex"}` {
		t.Fatalf("colorizeJSON changed text = %q", got)
	}
}

func detailActionModel() model {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	return model{
		ctx:    context.Background(),
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewInspector,
		width:  120,
		height: 24,
		now:    now,
		index:  map[uint64]int{1: 0},
		requests: []requestItem{{
			ID:         1,
			Method:     "POST",
			RequestURI: "/api/users",
			Host:       "alex.tun.aresa.me",
			StatusCode: 201,
			State:      client.EventRequestCompleted,
			StartedAt:  now,
			RequestHeader: map[string][]string{
				"Content-Type": {"application/json"},
			},
			ResponseHeader: map[string][]string{
				"Content-Type": {"application/json"},
			},
			RequestPreview: client.BodyPreview{
				Size:        int64(len(`{"name":"Alex"}`)),
				Captured:    int64(len(`{"name":"Alex"}`)),
				Text:        `{"name":"Alex"}`,
				ContentType: "application/json",
			},
			ResponsePreview: client.BodyPreview{
				Size:        int64(len(`{"ok":true}`)),
				Captured:    int64(len(`{"ok":true}`)),
				Text:        `{"ok":true}`,
				ContentType: "application/json",
			},
		}},
	}
}

func TestRequestWindowKeepsSelectedRowVisible(t *testing.T) {
	start := requestWindowStart(50, 25, 10)
	if start > 25 || start+10 <= 25 {
		t.Fatalf("window [%d,%d) does not include selected row 25", start, start+10)
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("żółć-request-path", 8)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncate = %q, want ellipsis suffix", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("truncate produced invalid replacement rune: %q", got)
	}
}

func TestRelativeAgeKeepsSecondsAfterMinute(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)

	if got := relativeAge(now, now.Add(-75*time.Second)); got != "1m15s" {
		t.Fatalf("relativeAge = %q, want %q", got, "1m15s")
	}
	if got := relativeAge(now.Add(time.Second), now.Add(-75*time.Second)); got != "1m16s" {
		t.Fatalf("relativeAge after tick = %q, want %q", got, "1m16s")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{
		0:    "0B",
		999:  "999B",
		1400: "1.4kb",
	}
	for size, want := range tests {
		if got := formatBytes(size); got != want {
			t.Fatalf("formatBytes(%d) = %q, want %q", size, got, want)
		}
	}
}

func lastLine(view string) string {
	plain := stripANSI(view)
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	return lines[len(lines)-1]
}
