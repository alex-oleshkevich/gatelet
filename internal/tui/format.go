package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

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
	var b strings.Builder
	col := 0
	written := 0
	for i := 0; i < len(s); {
		r, size := rune(s[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		if r == '\x1b' {
			end := ansiEnd(s, i)
			if col >= start && written < width {
				b.WriteString(s[i:end])
			}
			i = end
			continue
		}
		if col >= start && written < width {
			b.WriteString(s[i : i+size])
			written++
		}
		col++
		if written >= width {
			break
		}
		i += size
	}
	return b.String()
}

func ansiEnd(s string, start int) int {
	if start+1 >= len(s) {
		return start + 1
	}
	if s[start+1] == '[' {
		for i := start + 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	}
	return start + 1
}

func truncateVisible(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= limit {
		return s
	}
	return truncate(stripANSI(s), limit)
}

func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inEscape {
			if c >= '@' && c <= '~' {
				inEscape = false
			}
			continue
		}
		if c == 0x1b {
			inEscape = true
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
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

func previewLimitForDisplay() int64 {
	return int64(client.DefaultPreviewLimit)
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
