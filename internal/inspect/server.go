package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gatelet/internal/client"
)

type Config struct {
	Name         string
	PublicURL    string
	Target       string
	Token        string
	PreviewLimit int
	Replay       func(context.Context, string, client.RequestEvent) (client.RequestEvent, error)
}

type Store struct {
	mu          sync.RWMutex
	requests    []client.RequestEvent
	index       map[uint64]int
	subscribers map[chan client.RequestEvent]struct{}
}

func NewStore() *Store {
	return &Store{
		index:       make(map[uint64]int),
		subscribers: make(map[chan client.RequestEvent]struct{}),
	}
}

func (s *Store) Apply(event client.RequestEvent) {
	if event.ID == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if i, ok := s.index[event.ID]; ok {
		mergeEvent(&s.requests[i], event)
		s.publishLocked(s.requests[i])
		return
	}
	s.index[event.ID] = len(s.requests)
	s.requests = append(s.requests, event)
	s.publishLocked(event)
}

func (s *Store) Requests() []client.RequestEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return slices.Clone(s.requests)
}

func (s *Store) Filter(query RequestQuery) []client.RequestEvent {
	events := s.Requests()
	filtered := make([]client.RequestEvent, 0, len(events))
	for _, event := range events {
		if query.SinceID != 0 && event.ID <= query.SinceID {
			continue
		}
		if query.Method != "" && !strings.EqualFold(event.Method, query.Method) {
			continue
		}
		if query.Status != 0 && event.StatusCode != query.Status {
			continue
		}
		if query.ErrorKind != "" && event.ErrorKind != query.ErrorKind {
			continue
		}
		if query.PathContains != "" && !strings.Contains(event.RequestURI, query.PathContains) {
			continue
		}
		filtered = append(filtered, event)
		if query.Limit > 0 && len(filtered) >= query.Limit {
			break
		}
	}
	return filtered
}

func (s *Store) Get(id uint64) (client.RequestEvent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	i, ok := s.index[id]
	if !ok {
		return client.RequestEvent{}, false
	}
	return s.requests[i], true
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.requests)
}

func (s *Store) Subscribe() (<-chan client.RequestEvent, func()) {
	ch := make(chan client.RequestEvent, 16)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		close(ch)
		s.mu.Unlock()
	}
	return ch, cancel
}

func (s *Store) publishLocked(event client.RequestEvent) {
	for ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

type RequestQuery struct {
	Limit        int
	SinceID      uint64
	Method       string
	Status       int
	ErrorKind    client.ErrorKind
	PathContains string
}

type Server struct {
	config Config
	store  *Store
	pause  *client.PauseController
}

func NewServer(config Config, store *Store, pause *client.PauseController) *Server {
	return &Server{
		config: config,
		store:  store,
		pause:  pause,
	}
}

func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("parse inspect address: %w", err)
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("inspect API address must bind to a loopback host")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen inspect API: %w", err)
	}
	return ln, nil
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpServer := &http.Server{Handler: s.Handler()}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = httpServer.Shutdown(shutdownCtx)
			cancel()
		case <-done:
		}
	}()
	err := httpServer.Serve(ln)
	close(done)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/capabilities", s.handleCapabilities)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/requests", s.handleRequests)
	mux.HandleFunc("GET /api/requests/{id}", s.handleRequest)
	mux.HandleFunc("POST /api/requests/{id}/replay", s.handleReplay)
	mux.HandleFunc("POST /api/pause", s.handlePause)
	mux.HandleFunc("POST /api/resume", s.handleResume)
	mux.HandleFunc("GET /openapi.json", s.handleOpenAPI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "inspect token is required")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type statusResponse struct {
	PublicURL    string `json:"public_url"`
	Target       string `json:"target"`
	Paused       bool   `json:"paused"`
	QueueDepth   int    `json:"queue_depth"`
	RequestCount int    `json:"request_count"`
}

type capabilitiesResponse struct {
	APIVersion   string             `json:"api_version"`
	Name         string             `json:"name,omitempty"`
	PublicURL    string             `json:"public_url,omitempty"`
	Target       string             `json:"target,omitempty"`
	PreviewLimit int                `json:"preview_limit"`
	Auth         authCapabilities   `json:"auth"`
	Endpoints    []string           `json:"endpoints"`
	Actions      []string           `json:"actions"`
	Schemas      map[string]string  `json:"schemas"`
	Commands     []commandReference `json:"commands"`
}

type authCapabilities struct {
	MutationsRequireToken bool   `json:"mutations_require_token"`
	Header                string `json:"header,omitempty"`
}

type commandReference struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type requestResponse struct {
	ID              uint64              `json:"id"`
	Type            client.EventType    `json:"type"`
	Time            string              `json:"time"`
	Method          string              `json:"method,omitempty"`
	RequestURI      string              `json:"request_uri,omitempty"`
	PublicURL       string              `json:"public_url,omitempty"`
	TargetURL       string              `json:"target_url,omitempty"`
	Host            string              `json:"host,omitempty"`
	RemoteAddr      string              `json:"remote_addr,omitempty"`
	RequestHeader   http.Header         `json:"request_header,omitempty"`
	ResponseHeader  http.Header         `json:"response_header,omitempty"`
	RequestPreview  bodyPreviewResponse `json:"request_preview"`
	ResponsePreview bodyPreviewResponse `json:"response_preview"`
	HTTPBasicAuth   bool                `json:"http_basic_auth"`
	StatusCode      int                 `json:"status_code,omitempty"`
	RequestSize     int64               `json:"request_size"`
	ResponseSize    int64               `json:"response_size"`
	DurationMS      float64             `json:"duration_ms"`
	QueueDepth      int                 `json:"queue_depth,omitempty"`
	Error           string              `json:"error,omitempty"`
	ErrorKind       client.ErrorKind    `json:"error_kind,omitempty"`
}

type bodyPreviewResponse struct {
	Text        string `json:"text,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size"`
	Captured    int64  `json:"captured"`
	Omitted     bool   `json:"omitted"`
	Reason      string `json:"reason,omitempty"`
}

type errorResponse struct {
	OK    bool            `json:"ok"`
	Error structuredError `json:"error"`
}

type structuredError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.capabilities())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.status())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unavailable", "event streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	events, cancel := s.store.Subscribe()
	defer cancel()
	for {
		select {
		case event := <-events:
			data, err := json.Marshal(requestDTO(event))
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\n", event.Type)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	query, err := parseRequestQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	events := s.store.Filter(query)
	requests := make([]requestResponse, 0, len(events))
	for _, event := range events {
		requests = append(requests, requestDTO(event))
	}
	writeJSON(w, requests)
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	event, ok := s.requestByID(w, r)
	if !ok {
		return
	}
	writeJSON(w, requestDTO(event))
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMutation(w, r) {
		return
	}
	event, ok := s.requestByID(w, r)
	if !ok {
		return
	}

	replay := s.config.Replay
	if replay == nil {
		replay = client.ReplayRequest
	}
	result, err := replay(r.Context(), s.config.Target, event)
	if err != nil {
		writeError(w, http.StatusBadGateway, "replay_failed", err.Error())
		return
	}
	s.store.Apply(result)
	writeJSON(w, requestDTO(result))
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMutation(w, r) {
		return
	}
	s.pause.SetPaused(true)
	writeJSON(w, s.status())
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMutation(w, r) {
		return
	}
	s.pause.SetPaused(false)
	writeJSON(w, s.status())
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, openAPIDocument())
}

func (s *Server) capabilities() capabilitiesResponse {
	previewLimit := s.config.PreviewLimit
	if previewLimit == 0 {
		previewLimit = client.DefaultPreviewLimit
	}
	return capabilitiesResponse{
		APIVersion:   "v1",
		Name:         s.config.Name,
		PublicURL:    s.config.PublicURL,
		Target:       s.config.Target,
		PreviewLimit: previewLimit,
		Auth: authCapabilities{
			MutationsRequireToken: s.config.Token != "",
			Header:                "Authorization: Bearer <token>",
		},
		Endpoints: []string{
			"/api/capabilities",
			"/api/status",
			"/api/events",
			"/api/requests",
			"/api/requests/{id}",
			"/api/requests/{id}/replay",
			"/api/pause",
			"/api/resume",
			"/openapi.json",
		},
		Actions: []string{"pause", "resume", "replay"},
		Schemas: map[string]string{"openapi": "/openapi.json"},
		Commands: []commandReference{
			{Name: "gatelet prime", Description: "Print an agent briefing for a running client"},
			{Name: "gatelet inspect", Description: "Query and control the running client inspect API"},
		},
	}
}

func (s *Server) status() statusResponse {
	status := statusResponse{
		PublicURL:    s.config.PublicURL,
		Target:       s.config.Target,
		RequestCount: s.store.Count(),
	}
	if s.pause != nil {
		status.Paused = s.pause.IsPaused()
		status.QueueDepth = s.pause.QueueDepth()
	}
	return status
}

func (s *Server) requestByID(w http.ResponseWriter, r *http.Request) (client.RequestEvent, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "request not found")
		return client.RequestEvent{}, false
	}
	event, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "request not found")
		return client.RequestEvent{}, false
	}
	return event, true
}

func (s *Server) authorizeMutation(w http.ResponseWriter, r *http.Request) bool {
	if s.authorized(r) {
		return true
	}
	writeError(w, http.StatusUnauthorized, "unauthorized", "inspect token is required")
	return false
}

func (s *Server) authorized(r *http.Request) bool {
	if s.config.Token == "" {
		return true
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.Header.Get("X-Gatelet-Inspect-Token")
	}
	return token == s.config.Token
}

func parseRequestQuery(r *http.Request) (RequestQuery, error) {
	values := r.URL.Query()
	var query RequestQuery
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return RequestQuery{}, fmt.Errorf("limit must be a non-negative integer")
		}
		query.Limit = limit
	}
	if raw := values.Get("since_id"); raw != "" {
		sinceID, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return RequestQuery{}, fmt.Errorf("since_id must be a non-negative integer")
		}
		query.SinceID = sinceID
	}
	if raw := values.Get("status"); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil {
			return RequestQuery{}, fmt.Errorf("status must be an integer")
		}
		query.Status = status
	}
	query.Method = values.Get("method")
	query.ErrorKind = client.ErrorKind(values.Get("error_kind"))
	query.PathContains = values.Get("path_contains")
	return query, nil
}

func requestDTO(event client.RequestEvent) requestResponse {
	return requestResponse{
		ID:              event.ID,
		Type:            event.Type,
		Time:            formatTime(event.Time),
		Method:          event.Method,
		RequestURI:      event.RequestURI,
		PublicURL:       event.PublicURL,
		TargetURL:       event.TargetURL,
		Host:            event.Host,
		RemoteAddr:      event.RemoteAddr,
		RequestHeader:   event.RequestHeader,
		ResponseHeader:  event.ResponseHeader,
		RequestPreview:  bodyPreviewDTO(event.RequestPreview),
		ResponsePreview: bodyPreviewDTO(event.ResponsePreview),
		HTTPBasicAuth:   event.HTTPBasicAuth,
		StatusCode:      event.StatusCode,
		RequestSize:     event.RequestSize,
		ResponseSize:    event.ResponseSize,
		DurationMS:      float64(event.Duration) / float64(time.Millisecond),
		QueueDepth:      event.QueueDepth,
		Error:           event.Error,
		ErrorKind:       event.ErrorKind,
	}
}

func bodyPreviewDTO(preview client.BodyPreview) bodyPreviewResponse {
	return bodyPreviewResponse{
		Text:        preview.Text,
		ContentType: preview.ContentType,
		Size:        preview.Size,
		Captured:    preview.Captured,
		Omitted:     preview.Omitted,
		Reason:      preview.Reason,
	}
}

func mergeEvent(dst *client.RequestEvent, src client.RequestEvent) {
	if src.Type != "" {
		dst.Type = src.Type
	}
	if !src.Time.IsZero() {
		dst.Time = src.Time
	}
	if src.Method != "" {
		dst.Method = src.Method
	}
	if src.RequestURI != "" {
		dst.RequestURI = src.RequestURI
	}
	if src.PublicURL != "" {
		dst.PublicURL = src.PublicURL
	}
	if src.TargetURL != "" {
		dst.TargetURL = src.TargetURL
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
	if src.HTTPBasicAuth {
		dst.HTTPBasicAuth = true
	}
	if src.StatusCode != 0 {
		dst.StatusCode = src.StatusCode
	}
	if src.RequestSize != 0 {
		dst.RequestSize = src.RequestSize
	}
	if src.ResponseSize != 0 {
		dst.ResponseSize = src.ResponseSize
	}
	if src.Duration != 0 {
		dst.Duration = src.Duration
	}
	if src.QueueDepth != 0 {
		dst.QueueDepth = src.QueueDepth
	}
	if src.Error != "" {
		dst.Error = src.Error
	}
	if src.ErrorKind != "" {
		dst.ErrorKind = src.ErrorKind
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = errors.New("encode response").Error()
		}
		http.Error(w, message, http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		OK: false,
		Error: structuredError{
			Code:    code,
			Message: message,
		},
	})
}

func openAPIDocument() map[string]any {
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "Gatelet Inspect API",
			"version": "v1",
		},
		"paths": map[string]any{
			"/api/capabilities":  map[string]any{"get": map[string]any{"summary": "Describe agent capabilities"}},
			"/api/status":        map[string]any{"get": map[string]any{"summary": "Return tunnel status"}},
			"/api/events":        map[string]any{"get": map[string]any{"summary": "Stream request events"}},
			"/api/requests":      map[string]any{"get": map[string]any{"summary": "List captured requests"}},
			"/api/requests/{id}": map[string]any{"get": map[string]any{"summary": "Return request detail"}},
			"/api/requests/{id}/replay": map[string]any{
				"post": map[string]any{"summary": "Replay a captured request"},
			},
			"/api/pause":  map[string]any{"post": map[string]any{"summary": "Pause request forwarding"}},
			"/api/resume": map[string]any{"post": map[string]any{"summary": "Resume request forwarding"}},
		},
	}
}
