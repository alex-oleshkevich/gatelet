package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gatelet/internal/client"
)

type viewMode int

const (
	viewList viewMode = iota
	viewDetail
	viewBody
)

type requestItem struct {
	ID              uint64
	Method          string
	RequestURI      string
	Host            string
	RemoteAddr      string
	RequestHeader   map[string][]string
	ResponseHeader  map[string][]string
	RequestPreview  client.BodyPreview
	ResponsePreview client.BodyPreview
	StatusCode      int
	RequestSize     int64
	ResponseSize    int64
	Duration        time.Duration
	State           client.EventType
	Error           string
	ErrorKind       client.ErrorKind
	StartedAt       time.Time
	LastUpdate      time.Time
}

type targetHealth string

const (
	targetHealthUnknown  targetHealth = "unknown"
	targetHealthOK       targetHealth = "ok"
	targetHealthDown     targetHealth = "down"
	targetHealthDegraded targetHealth = "degraded"
)

type model struct {
	ctx       context.Context
	cancel    context.CancelFunc
	events    <-chan client.RequestEvent
	clientErr <-chan error
	pause     *client.PauseController

	url          string
	target       string
	captureDir   string
	status       string
	targetHealth targetHealth
	message      string
	paused       bool
	mode         viewMode
	filtering    bool
	filter       string
	plainBody    bool
	selected     int
	detailScroll int
	bodyScroll   int
	width        int
	height       int
	now          time.Time

	requests []requestItem
	index    map[uint64]int

	copyText func(string) error
	replay   func(context.Context, string, client.RequestEvent) (client.RequestEvent, error)
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.events), waitClient(m.clientErr), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.filtering {
		return m.updateFilter(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()
	case eventMsg:
		m.applyEvent(client.RequestEvent(msg))
		m.clampSelection()
		return m, waitEvent(m.events)
	case clientDoneMsg:
		if msg.err != nil && m.ctx.Err() == nil {
			m.status = "disconnected"
			m.message = msg.err.Error()
			return m, nil
		}
		m.status = "stopped"
		return m, nil
	case replayDoneMsg:
		if msg.event.ID != 0 {
			m.applyEvent(msg.event)
			m.clampSelection()
		}
		if msg.err != nil {
			m.message = "replay failed: " + msg.err.Error()
			return m, nil
		}
		m.message = fmt.Sprintf("replay %d %s", msg.event.StatusCode, msg.event.RequestURI)
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.cancel()
		return m, tea.Quit
	case "esc":
		if m.mode == viewBody {
			m.mode = viewDetail
			m.bodyScroll = 0
			m.message = ""
		} else if m.mode == viewDetail {
			m.mode = viewList
			m.detailScroll = 0
			m.message = ""
		}
	case "p", "P":
		m.paused = m.pause.Toggle()
		if m.paused {
			m.message = "paused: new requests will wait"
		} else {
			m.message = "resumed: queued requests are forwarding"
		}
	case "enter":
		if m.mode == viewList && len(m.visibleRequests()) > 0 {
			m.mode = viewDetail
			m.detailScroll = 0
			m.message = ""
		}
	case "b":
		if m.mode == viewDetail {
			m.mode = viewBody
			m.bodyScroll = 0
			m.message = ""
		} else if m.mode == viewBody {
			m.mode = viewDetail
			m.bodyScroll = 0
			m.message = ""
		}
	case "/":
		if m.mode == viewList {
			m.filtering = true
			m.message = ""
		}
	case "c":
		if err := m.copyTextFunc()(m.url); err != nil {
			m.message = "copy failed: " + err.Error()
		} else {
			m.message = "copied " + m.url
		}
	case "y":
		if m.mode == viewDetail {
			m.copySelectedCurl()
		}
	case "e":
		if m.mode == viewDetail {
			m.exportSelectedCurl()
		}
	case "r":
		if m.mode == viewDetail {
			return m.replaySelectedRequest()
		}
	case "x":
		if m.mode == viewList {
			m.requests = nil
			m.index = make(map[uint64]int)
			m.selected = 0
			m.message = "history cleared"
		}
	case "up", "k":
		if m.mode == viewBody {
			m.bodyScroll--
		} else if m.mode == viewDetail {
			m.detailScroll--
		} else if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.mode == viewBody {
			m.bodyScroll++
		} else if m.mode == viewDetail {
			m.detailScroll++
		} else if m.selected < len(m.visibleRequests())-1 {
			m.selected++
		}
	case "pgup", "u":
		if m.mode == viewBody {
			m.bodyScroll -= max(1, m.bodyHeight())
		} else if m.mode == viewDetail {
			m.detailScroll -= max(1, m.detailHeight())
		}
	case "pgdown", "d", " ":
		if m.mode == viewBody {
			m.bodyScroll += max(1, m.bodyHeight())
		} else if m.mode == viewDetail {
			m.detailScroll += max(1, m.detailHeight())
		}
	case "f", "F":
		if m.mode == viewBody {
			m.plainBody = !m.plainBody
			m.bodyScroll = 0
		} else if m.mode == viewDetail {
			m.plainBody = !m.plainBody
			m.detailScroll = 0
		}
	case "home", "g":
		if m.mode == viewBody {
			m.bodyScroll = 0
		} else if m.mode == viewDetail {
			m.detailScroll = 0
		} else {
			m.selected = 0
		}
	case "end", "G":
		if m.mode == viewList {
			m.selected = len(m.visibleRequests()) - 1
		}
	}
	m.clampSelection()
	m.clampDetailScroll()
	m.clampBodyScroll()
	return m, nil
}

func (m model) updateFilter(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
	case "ctrl+c":
		m.cancel()
		return m, tea.Quit
	case "esc":
		m.filtering = false
		m.filter = ""
		m.message = "filter cleared"
	case "enter":
		m.filtering = false
		if m.filter == "" {
			m.message = "filter cleared"
		} else {
			m.message = "filter: " + m.filter
		}
	case "ctrl+u":
		m.filter = ""
	case "backspace":
		if len(m.filter) > 0 {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
		}
	default:
		if len(key.String()) == 1 {
			m.filter += key.String()
		}
	}
	m.clampSelection()
	return m, nil
}

func (m *model) applyEvent(event client.RequestEvent) {
	m.status = "online"
	item := requestItem{
		ID:              event.ID,
		Method:          event.Method,
		RequestURI:      event.RequestURI,
		Host:            event.Host,
		RemoteAddr:      event.RemoteAddr,
		RequestHeader:   event.RequestHeader,
		ResponseHeader:  event.ResponseHeader,
		RequestPreview:  event.RequestPreview,
		ResponsePreview: event.ResponsePreview,
		StatusCode:      event.StatusCode,
		RequestSize:     event.RequestSize,
		ResponseSize:    event.ResponseSize,
		Duration:        event.Duration,
		State:           event.Type,
		Error:           event.Error,
		ErrorKind:       event.ErrorKind,
		StartedAt:       event.Time,
		LastUpdate:      event.Time,
	}
	if item.StartedAt.IsZero() {
		item.StartedAt = m.now
		item.LastUpdate = m.now
	}

	if idx, ok := m.index[event.ID]; ok {
		current := m.requests[idx]
		mergeRequestItem(&current, item)
		m.requests[idx] = current
		m.updateTargetHealth(current)
		return
	}

	m.requests = append([]requestItem{item}, m.requests...)
	m.updateTargetHealth(item)
	m.rebuildIndex()
	if len(m.requests) > maxRequests {
		m.requests = m.requests[:maxRequests]
		m.rebuildIndex()
	}
}

func mergeRequestItem(dst *requestItem, src requestItem) {
	dst.State = src.State
	dst.LastUpdate = src.LastUpdate
	if src.StatusCode != 0 {
		dst.StatusCode = src.StatusCode
	}
	if src.Duration != 0 {
		dst.Duration = src.Duration
	}
	if src.Error != "" {
		dst.Error = src.Error
	}
	if src.ErrorKind != "" {
		dst.ErrorKind = src.ErrorKind
	}
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.RemoteAddr != "" {
		dst.RemoteAddr = src.RemoteAddr
	}
	if len(src.RequestHeader) > 0 {
		dst.RequestHeader = src.RequestHeader
	}
	if len(src.ResponseHeader) > 0 {
		dst.ResponseHeader = src.ResponseHeader
	}
	if src.RequestPreview.Size > 0 || src.RequestPreview.Omitted {
		dst.RequestPreview = src.RequestPreview
	}
	if src.ResponsePreview.Size > 0 || src.ResponsePreview.Omitted {
		dst.ResponsePreview = src.ResponsePreview
	}
	if src.RequestSize > 0 {
		dst.RequestSize = src.RequestSize
	}
	if src.ResponseSize > 0 {
		dst.ResponseSize = src.ResponseSize
	}
}

func (m *model) updateTargetHealth(item requestItem) {
	switch {
	case item.ErrorKind == client.ErrorKindLocalTarget:
		m.targetHealth = targetHealthDown
	case item.State == client.EventRequestCompleted && item.StatusCode >= 500:
		m.targetHealth = targetHealthDegraded
	case item.State == client.EventRequestCompleted && item.StatusCode > 0:
		m.targetHealth = targetHealthOK
	}
}

func (m *model) rebuildIndex() {
	m.index = make(map[uint64]int, len(m.requests))
	for i := range m.requests {
		m.index[m.requests[i].ID] = i
	}
}

func (m model) visibleRequests() []requestItem {
	if m.filter == "" {
		return m.requests
	}
	terms := strings.Fields(strings.ToLower(m.filter))
	var out []requestItem
	for _, item := range m.requests {
		haystack := strings.ToLower(item.Method + " " + item.RequestURI + " " + item.Host + " " + remoteIP(item.RemoteAddr) + " " + fmt.Sprint(item.StatusCode) + " " + item.Error)
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, item)
		}
	}
	return out
}

func (m model) selectedRequest() (requestItem, bool) {
	visible := m.visibleRequests()
	if len(visible) == 0 || m.selected < 0 || m.selected >= len(visible) {
		return requestItem{}, false
	}
	return visible[m.selected], true
}

func (m *model) clampSelection() {
	visible := m.visibleRequests()
	if len(visible) == 0 {
		m.selected = 0
		m.detailScroll = 0
		return
	}
	if m.selected >= len(visible) {
		m.selected = len(visible) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
}

func (m *model) clampDetailScroll() {
	if m.mode != viewDetail {
		return
	}
	item, ok := m.selectedRequest()
	if !ok {
		m.detailScroll = 0
		return
	}
	lines := strings.Split(strings.TrimRight(formatDetail(item, max(20, m.width), m.now, m.plainBody), "\n"), "\n")
	maxScroll := max(0, len(lines)-m.detailHeight())
	if m.detailScroll > maxScroll {
		m.detailScroll = maxScroll
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
}

func (m *model) clampBodyScroll() {
	if m.mode != viewBody {
		return
	}
	item, ok := m.selectedRequest()
	if !ok {
		m.bodyScroll = 0
		return
	}
	lines := strings.Split(strings.TrimRight(formatBodyView(item, max(20, m.width), m.plainBody), "\n"), "\n")
	maxScroll := max(0, len(lines)-m.bodyHeight())
	if m.bodyScroll > maxScroll {
		m.bodyScroll = maxScroll
	}
	if m.bodyScroll < 0 {
		m.bodyScroll = 0
	}
}
