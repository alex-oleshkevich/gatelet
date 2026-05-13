package tui

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"

	"gatelet/internal/client"
)

func (m model) copyTextFunc() func(string) error {
	if m.copyText != nil {
		return m.copyText
	}
	return defaultCopyText
}

func (m model) replayFunc() func(context.Context, string, client.RequestEvent) (client.RequestEvent, error) {
	if m.replay != nil {
		return m.replay
	}
	return client.ReplayRequest
}

func (m *model) copySelectedCurl() {
	event, ok := m.selectedEvent()
	if !ok {
		m.message = "no request selected"
		return
	}
	command, err := client.CurlCommand(event, m.url)
	if err != nil {
		m.message = "curl failed: " + err.Error()
		return
	}
	if err := m.copyTextFunc()(command); err != nil {
		m.message = "copy failed: " + err.Error()
		return
	}
	m.message = "copied curl"
}

func (m *model) exportSelectedCurl() {
	event, ok := m.selectedEvent()
	if !ok {
		m.message = "no request selected"
		return
	}
	command, err := client.CurlCommand(event, m.url)
	if err != nil {
		m.message = "curl failed: " + err.Error()
		return
	}
	dir := m.captureDir
	if dir == "" {
		dir = defaultCaptureDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.message = "save failed: " + err.Error()
		return
	}
	path := filepath.Join(dir, curlFilename(event))
	if err := os.WriteFile(path, []byte(command+"\n"), 0o600); err != nil {
		m.message = "save failed: " + err.Error()
		return
	}
	m.message = "saved curl " + path
}

func (m model) replaySelectedRequest() (tea.Model, tea.Cmd) {
	event, ok := m.selectedEvent()
	if !ok {
		m.message = "no request selected"
		return m, nil
	}
	target := m.target
	if target == "" {
		m.message = "replay failed: local target unavailable"
		return m, nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	replay := m.replayFunc()
	m.message = "replaying " + event.RequestLine()
	return m, func() tea.Msg {
		result, err := replay(ctx, target, event)
		return replayDoneMsg{event: result, err: err}
	}
}

func (m model) selectedEvent() (client.RequestEvent, bool) {
	item, ok := m.selectedRequest()
	if !ok {
		return client.RequestEvent{}, false
	}
	return client.RequestEvent{
		ID:              item.ID,
		Type:            item.State,
		Time:            item.StartedAt,
		Method:          item.Method,
		RequestURI:      item.RequestURI,
		Host:            item.Host,
		RemoteAddr:      item.RemoteAddr,
		RequestHeader:   http.Header(item.RequestHeader),
		ResponseHeader:  http.Header(item.ResponseHeader),
		RequestPreview:  item.RequestPreview,
		ResponsePreview: item.ResponsePreview,
		StatusCode:      item.StatusCode,
		RequestSize:     item.RequestSize,
		ResponseSize:    item.ResponseSize,
		Duration:        item.Duration,
		Error:           item.Error,
	}, true
}

func defaultCopyText(text string) error {
	cmd := exec.Command("wl-copy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err == nil {
		return nil
	}
	return clipboard.WriteAll(text)
}

func defaultCaptureDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return "gatelet-captures"
	}
	return filepath.Join(dir, "gatelet", "captures")
}

func curlFilename(event client.RequestEvent) string {
	method := strings.ToLower(event.Method)
	if method == "" {
		method = "request"
	}
	requestPath := event.RequestURI
	if u, err := url.Parse(event.RequestURI); err == nil && u.Path != "" {
		requestPath = u.Path
	}
	slug := slugify(requestPath)
	if slug == "" {
		slug = "root"
	}
	return fmt.Sprintf("%06d-%s-%s.curl", event.ID, method, slug)
}

func slugify(value string) string {
	var b bytes.Buffer
	lastDash := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
