package tui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const targetProbeTimeout = 1500 * time.Millisecond

type targetProbeMsg struct {
	health targetHealth
}

func probeTargetHealth(target string) tea.Cmd {
	return func() tea.Msg {
		return targetProbeMsg{health: checkTargetHealth(target)}
	}
}

func checkTargetHealth(target string) targetHealth {
	probeURL, ok := normalizeTargetProbeURL(target)
	if !ok {
		return targetHealthDown
	}

	ctx, cancel := context.WithTimeout(context.Background(), targetProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, probeURL, nil)
	if err != nil {
		return targetHealthDown
	}
	req.Header.Set("User-Agent", "gatelet-target-probe")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return targetHealthDown
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return targetHealthDegraded
	}
	return targetHealthOK
}

func normalizeTargetProbeURL(target string) (string, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false
	}
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), true
}
