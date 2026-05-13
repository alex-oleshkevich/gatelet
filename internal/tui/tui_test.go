package tui

import (
	"strings"
	"testing"
	"time"

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

func TestColorizeJSONKeepsPlainText(t *testing.T) {
	got := stripANSI(colorizeJSON(`{"ok":true,"n":12,"name":"Alex"}`))
	if got != `{"ok":true,"n":12,"name":"Alex"}` {
		t.Fatalf("colorizeJSON changed text = %q", got)
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
