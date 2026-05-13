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
	if m.mode == viewBody {
		body = m.renderBody(width, bodyHeight)
	} else if m.mode == viewDetail {
		body = m.renderDetail(width, bodyHeight)
	} else {
		body = m.renderList(width, bodyHeight)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, footer)
}

func (m model) renderHeader(width int) string {
	left := titleStyle.Render("gatelet") + " " + urlStyle.Render(m.url)
	if m.mode == viewBody {
		left = titleStyle.Render("gatelet") + " " + headStyle.Render("body viewer")
		if item, ok := m.selectedRequest(); ok {
			left += " " + mutedStyle.Render(item.Method+" "+item.RequestURI)
		}
	} else if m.mode == viewDetail {
		left = titleStyle.Render("gatelet") + " " + headStyle.Render("request detail")
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
	mode := mutedStyle.Render("running")
	if m.paused {
		mode = queuedStyle.Render("paused")
	}
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
	if m.mode == viewDetail {
		if item, ok := m.selectedRequest(); ok {
			mode := "FORMAT"
			if m.plainBody {
				mode = "PLAIN"
			}
			return "DETAIL " + item.Method + " " + mode
		}
		return "DETAIL"
	}
	if m.mode == viewBody {
		mode := "FORMAT"
		if m.plainBody {
			mode = "PLAIN"
		}
		return "BODY " + mode
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
			return "Esc back | F format | j/k scroll | q"
		}
		if m.mode == viewDetail {
			return "b body | Esc back | r replay | q"
		}
		return "Enter open | / filter | p pause | q"
	}
	if m.mode == viewBody {
		return "F format/plain | Esc detail | j/k scroll | q quit"
	}
	if m.mode == viewDetail {
		return "b body | r replay | y copy curl | e save curl | F format/plain | Esc back | j/k scroll | q quit"
	}
	return "Enter details | j/k move | / filter | x clear | c copy url | p pause | q quit"
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
