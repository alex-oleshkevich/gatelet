package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gatelet/internal/client"
)

func statusText(item requestItem) string {
	if item.Error != "" {
		return "ERR"
	}
	if item.StatusCode == 0 {
		if item.State == client.EventRequestQueued {
			return "queued"
		}
		return "-"
	}
	return fmt.Sprint(item.StatusCode)
}

func stateLabel(state client.EventType) string {
	switch state {
	case client.EventRequestReceived:
		return "received"
	case client.EventRequestQueued:
		return "queued"
	case client.EventRequestForwarding:
		return "forwarding"
	case client.EventResponseStarted:
		return "streaming"
	case client.EventRequestCompleted:
		return "completed"
	case client.EventRequestFailed:
		return "failed"
	default:
		return string(state)
	}
}

func styledStatus(item requestItem) string {
	status := statusText(item)
	return statusStyle(item).Render(status)
}

func padRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncateVisible(s, width)
	padding := width - lipgloss.Width(s)
	if padding <= 0 {
		return s
	}
	return s + strings.Repeat(" ", padding)
}

func visibleWindow(s string, start, width int) string {
	if width <= 0 {
		return ""
	}
	if start < 0 {
		start = 0
	}
	return ansi.Cut(s, start, start+width)
}

func wrapVisibleBlock(s string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if ansi.StringWidth(line) <= width {
			wrapped = append(wrapped, line)
			continue
		}
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(line, width, false), "\n")...)
	}
	return strings.Join(wrapped, "\n")
}

func truncateVisible(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= limit {
		return s
	}
	return ansi.Truncate(s, limit, "...")
}

func stripANSI(s string) string {
	return ansi.Strip(s)
}

func truncate(s string, limit int) string {
	if ansi.StringWidth(s) <= limit {
		return s
	}
	if limit <= 3 {
		return ansi.Truncate(s, limit, "")
	}
	return ansi.Truncate(s, limit, "...")
}

func remoteIP(remoteAddr string) string {
	host, _, ok := strings.Cut(remoteAddr, ":")
	if ok && host != "" {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(remoteAddr, "[]")
}

func relativeAge(now, started time.Time) string {
	if started.IsZero() {
		return "-"
	}
	if now.IsZero() {
		now = time.Now()
	}
	d := now.Sub(started)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func isOld(now, started time.Time) bool {
	if started.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Sub(started) >= oldRequestAt
}

func formatBytes(size int64) string {
	return client.FormatBytes(size)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
