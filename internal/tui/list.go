package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"gatelet/internal/client"
)

func (m model) renderList(width, height int) string {
	visible := m.visibleRequests()
	if len(visible) == 0 {
		message := "No incoming requests yet."
		if m.filter != "" {
			message = "No requests match /" + m.filter
		}
		return fitBlock(rowStyle.Render(mutedStyle.Render(message)), width, height)
	}

	limit := min(len(visible), max(1, height))
	start := requestWindowStart(len(visible), m.selected, limit)

	var b strings.Builder
	for row := 0; row < limit; row++ {
		i := start + row
		line := renderRequestRow(visible[i], width, m.now, i == m.selected)
		b.WriteString(line)
		if row < limit-1 {
			b.WriteString("\n")
		}
	}
	return fitBlock(b.String(), width, height)
}

func renderRequestRow(item requestItem, width int, now time.Time, selected bool) string {
	innerWidth := max(1, width-2)
	cols := requestColumns(innerWidth)

	switch {
	case selected:
		line := visibleWindow(requestRowLine(item, cols, now, false), 0, innerWidth)
		return selectedStyle.Render(line)
	default:
		line := visibleWindow(requestRowLine(item, cols, now, true), 0, innerWidth)
		return rowStyle.Render(line)
	}
}

func requestRowLine(item requestItem, cols requestColumnSet, now time.Time, styled bool) string {
	parts := make([]string, 0, 6)
	appendPart := func(text string, width int, style lipgloss.Style) {
		if width <= 0 {
			return
		}
		if styled {
			parts = append(parts, padStyled(text, width, style))
			return
		}
		parts = append(parts, padRight(text, width))
	}

	appendPart(item.Method, cols.method, methodStyle(item.Method))
	appendPart(item.RequestURI, cols.path, valStyle)
	appendPart(statusText(item), cols.status, statusStyle(item))
	appendPart(formatBytes(item.RequestSize), cols.size, valStyle)
	appendPart(remoteIP(item.RemoteAddr), cols.remote, valStyle)
	appendPart(relativeAge(now, item.StartedAt), cols.age, valStyle)
	return strings.Join(parts, " ")
}

type requestColumnSet struct {
	method int
	path   int
	status int
	size   int
	remote int
	age    int
}

func requestColumns(width int) requestColumnSet {
	switch {
	case width >= 112:
		return requestColumnSet{method: 7, path: width - 7 - 7 - 9 - 18 - 8 - 5, status: 7, size: 9, remote: 18, age: 8}
	case width >= 90:
		return requestColumnSet{method: 7, path: width - 7 - 7 - 9 - 15 - 8 - 5, status: 7, size: 9, remote: 15, age: 8}
	case width >= 70:
		return requestColumnSet{method: 7, path: width - 7 - 7 - 9 - 8 - 4, status: 7, size: 9, remote: 0, age: 8}
	default:
		return requestColumnSet{method: 6, path: max(12, width-6-7-8-3), status: 7, size: 8, remote: 0, age: 0}
	}
}

func padStyled(text string, width int, style lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	raw := padRight(text, width)
	return style.Render(raw)
}

func methodStyle(method string) lipgloss.Style {
	switch method {
	case "GET":
		return keyStyle
	case "POST", "PUT", "PATCH":
		return headStyle
	case "DELETE":
		return status5xxStyle
	default:
		return valStyle
	}
}

func statusStyle(item requestItem) lipgloss.Style {
	if item.Error != "" || item.StatusCode >= 500 {
		return status5xxStyle
	}
	if item.StatusCode >= 400 {
		return status4xxStyle
	}
	if item.StatusCode >= 300 {
		return status3xxStyle
	}
	if item.StatusCode >= 200 {
		return status2xxStyle
	}
	if item.State == client.EventRequestQueued {
		return queuedStyle
	}
	return mutedStyle
}

func requestWindowStart(total, selected, limit int) int {
	if total <= limit || limit <= 0 {
		return 0
	}
	start := selected - limit/2
	if start < 0 {
		return 0
	}
	maxStart := total - limit
	if start > maxStart {
		return maxStart
	}
	return start
}
