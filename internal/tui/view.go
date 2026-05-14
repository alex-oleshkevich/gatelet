package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m model) View() string {
	width := m.width
	if width <= 0 {
		width = 120
	}
	height := m.height
	if height <= 0 {
		height = 32
	}
	if height < 8 {
		height = 8
	}

	header := m.renderHeader(width)
	footer := m.renderStatusBar(width)
	bodyHeight := height - lipgloss.Height(header) - 1 - lipgloss.Height(footer)
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	var body string
	switch m.mode {
	case viewBody:
		body = m.renderBody(width, bodyHeight)
	case viewInspector:
		body = m.renderInspector(width, bodyHeight)
	default:
		body = m.renderList(width, bodyHeight)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, footer)
}

func (m model) renderHeader(width int) string {
	left := titleStyle.Render("gatelet") + " " + urlStyle.Render(m.url)
	switch m.mode {
	case viewBody:
		left = titleStyle.Render("gatelet") + " " + headStyle.Render(m.inspectorTabLabel()+" body")
		if item, ok := m.selectedRequest(); ok {
			left += " " + mutedStyle.Render(item.Method+" "+item.RequestURI)
		}
	case viewInspector:
		left = titleStyle.Render("gatelet") + " " + headStyle.Render(m.inspectorTabLabel()+" inspector")
		if item, ok := m.selectedRequest(); ok {
			left += " " + mutedStyle.Render(item.Method+" "+item.RequestURI)
		}
	}

	queueDepth := 0
	if m.pause != nil {
		queueDepth = m.pause.QueueDepth()
	}
	state := styledConnectionState(m.status)
	target := styledTargetHealth(m.targetHealth)
	mode := styledForwardingMode(m.status, m.paused)
	right := fmt.Sprintf("%s  %s  %s  %s", state, target, mode, mutedStyle.Render(fmt.Sprintf("queued %d", queueDepth)))

	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	return visibleWindow(line, 0, width)
}

func (m model) renderStatusBar(width int) string {
	left := m.statusLabel()
	help := m.statusHelp()
	if m.filtering {
		left = "FILTER"
		help = "/" + m.filter + "  Enter apply  Esc clear  Ctrl+U reset"
	} else if m.message != "" {
		help = m.message + " | " + help
	}

	leftPart := statusAccentStyle.Render(left)
	remaining := max(0, width-lipgloss.Width(leftPart))
	rightPart := statusBarStyle.Width(remaining).Render(truncateVisible(help, max(1, remaining-2)))
	return leftPart + rightPart
}

func (m model) statusLabel() string {
	if m.mode == viewInspector {
		if item, ok := m.selectedRequest(); ok {
			mode := "FORMAT"
			if m.plainBody {
				mode = "PLAIN"
			}
			return strings.ToUpper(m.inspectorTabLabel()) + " " + item.Method + " " + mode
		}
		return strings.ToUpper(m.inspectorTabLabel())
	}
	if m.mode == viewBody {
		return "BODY " + strings.ToUpper(m.inspectorTabLabel())
	}
	visible := len(m.visibleRequests())
	if visible == 0 {
		if m.paused {
			return "LIST PAUSED"
		}
		return "LIST"
	}
	label := fmt.Sprintf("LIST %d/%d", min(m.selected+1, visible), visible)
	if m.filter != "" {
		label += " /"
	}
	if m.paused {
		label += " PAUSED"
	}
	return label
}

func (m model) statusHelp() string {
	if m.width < 72 {
		if m.mode == viewBody {
			return "Esc back | F format | h/l tab | q"
		}
		if m.mode == viewInspector {
			return "h/l tabs | b body | r replay | q"
		}
		return "Enter open | / filter | p pause | q"
	}
	if m.mode == viewBody {
		return "F format/plain | h/l tab | Esc inspector | j/k scroll | q quit"
	}
	if m.mode == viewInspector {
		if m.inspectorTab == inspectorTabResponse {
			return "h request | b body | s save | F format/plain | Esc back | j/k scroll | q quit"
		}
		return "l response | b body | r replay | y copy curl | e save curl | F format/plain | Esc back | j/k scroll | q quit"
	}
	return "Enter details | j/k move | / filter | x clear | c copy url | p pause | q quit"
}

func (m model) inspectorTabLabel() string {
	if m.inspectorTab == inspectorTabResponse {
		return "response"
	}
	return "request"
}

func styledConnectionState(status string) string {
	label := strings.ToUpper(status)
	if status == "online" {
		return status2xxStyle.Render(label)
	}
	return queuedStyle.Render(label)
}

func styledTargetHealth(health targetHealth) string {
	if health == "" {
		health = targetHealthUnknown
	}
	label := "target " + strings.ToUpper(string(health))
	switch health {
	case targetHealthOK:
		return status2xxStyle.Render(label)
	case targetHealthDegraded:
		return status3xxStyle.Render(label)
	case targetHealthDown:
		return status5xxStyle.Render(label)
	default:
		return mutedStyle.Render(label)
	}
}

func styledForwardingMode(status string, paused bool) string {
	if paused {
		return queuedStyle.Render("paused")
	}
	if status != "online" {
		return mutedStyle.Render("idle")
	}
	return status2xxStyle.Render("accepting")
}

func fitBlock(s string, width, height int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = lipgloss.NewStyle().Width(width).Render(truncateVisible(line, width))
	}
	return strings.Join(lines, "\n")
}
