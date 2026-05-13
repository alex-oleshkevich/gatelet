package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)
	urlStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("111"))
	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))
	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("111"))
	valStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229"))
	headStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("81"))
	rowStyle = lipgloss.NewStyle().
			Padding(0, 1)
	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)
	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)
	statusAccentStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("60")).
				Padding(0, 1)
	status2xxStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("150"))
	status3xxStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("81"))
	status4xxStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("214"))
	status5xxStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("203"))
	queuedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("222"))
)
