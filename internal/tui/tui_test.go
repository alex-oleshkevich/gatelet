package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	for _, notWant := range []string{"Method", "Path", "Status", "Remote IP", "Request headers"} {
		if strings.Contains(plain, notWant) {
			t.Fatalf("list view included %q:\n%s", notWant, plain)
		}
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

func TestDetailViewShowsRequestDetails(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewDetail,
		width:  120,
		height: 20,
		now:    now,
		index:  make(map[uint64]int),
		requests: []requestItem{{
			ID:         1,
			Method:     "GET",
			RequestURI: "/api/users",
			Host:       "alex.tun.aresa.me",
			StatusCode: 200,
			State:      client.EventRequestCompleted,
			StartedAt:  now.Add(-5 * time.Second),
			RequestHeader: map[string][]string{
				"User-Agent": {"curl/8.7.1"},
			},
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"request detail", "Status", "Remote", "Request headers", "User-Agent: curl/8.7.1", "Esc back"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("Detail view missing %q:\n%s", want, plain)
		}
	}
}

func TestDetailViewFormatsJSONBody(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewDetail,
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
				Text:        `{"name":"Alex"}`,
				ContentType: "application/json",
			},
		}},
	}

	plain := stripANSI(m.View())
	for _, want := range []string{`Request body [formatted json]`, `"name": "Alex"`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("formatted JSON body missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `{"name":"Alex"}`) {
		t.Fatalf("formatted JSON body still includes raw compact JSON:\n%s", plain)
	}
}

func TestDetailViewFormatsJSONWithoutContentType(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:    "https://alex.tun.aresa.me",
		status: "online",
		mode:   viewDetail,
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
				Size: int64(len(`{"name":"Alex"}`)),
				Text: `{"name":"Alex"}`,
			},
		}},
	}

	plain := stripANSI(m.View())
	if !strings.Contains(plain, `"name": "Alex"`) {
		t.Fatalf("JSON without content type was not formatted:\n%s", plain)
	}
}

func TestDetailViewTogglesPlainJSONBody(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	m := model{
		url:       "https://alex.tun.aresa.me",
		status:    "online",
		mode:      viewDetail,
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
				Text:        `{"name":"Alex"}`,
				ContentType: "application/json",
			},
		}},
	}

	plain := stripANSI(m.View())
	if !strings.Contains(plain, `Request body [plain]`) || !strings.Contains(plain, `{"name":"Alex"}`) {
		t.Fatalf("plain body missing raw JSON:\n%s", plain)
	}
	if strings.Contains(plain, `"name": "Alex"`) {
		t.Fatalf("plain JSON body was formatted:\n%s", plain)
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
		mode:   viewDetail,
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
			RequestPreview: client.BodyPreview{
				Size:        int64(len(`{"name":"Alex"}`)),
				Text:        `{"name":"Alex"}`,
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

func TestRelativeAgeAndOldRequests(t *testing.T) {
	now := time.Date(2026, 5, 13, 17, 0, 0, 0, time.UTC)
	started := now.Add(-31 * time.Minute)

	if got := relativeAge(now, started); got != "31m" {
		t.Fatalf("relativeAge = %q, want %q", got, "31m")
	}
	if !isOld(now, started) {
		t.Fatal("isOld returned false for request older than 30 minutes")
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
