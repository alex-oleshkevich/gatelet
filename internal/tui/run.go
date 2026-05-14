package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gatelet/internal/client"
)

const (
	maxRequests  = 500
	tickInterval = time.Second
)

type eventMsg client.RequestEvent
type clientDoneMsg struct{ err error }
type tickMsg time.Time
type replayDoneMsg struct {
	sourceID uint64
	event    client.RequestEvent
	err      error
}

func Run(ctx context.Context, config client.Config) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan client.RequestEvent, 256)
	errs := make(chan error, 1)
	pause := client.NewPauseController()

	config.Events = events
	config.PauseController = pause

	go func() {
		errs <- client.Run(runCtx, config)
	}()

	m := model{
		ctx:           runCtx,
		cancel:        cancel,
		events:        events,
		clientErr:     errs,
		pause:         pause,
		url:           tuiPublicURL(config),
		target:        config.Target,
		httpBasicAuth: config.HTTPBasicAuthEnabled(),
		status:        "connecting",
		targetHealth:  targetHealthUnknown,
		now:           time.Now(),
		index:         make(map[uint64]int),
		captureDir:    defaultCaptureDir(),
	}

	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func tuiPublicURL(config client.Config) string {
	return client.PublicURL(config.Name, config.Domain, config.ServerAddr)
}

func waitEvent(events <-chan client.RequestEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return nil
		}
		return eventMsg(event)
	}
}

func waitClient(errs <-chan error) tea.Cmd {
	return func() tea.Msg {
		return clientDoneMsg{err: <-errs}
	}
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
