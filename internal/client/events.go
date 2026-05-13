package client

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const DefaultPauseTimeout = 60 * time.Second

type EventType string

const (
	EventRequestReceived   EventType = "request_received"
	EventRequestQueued     EventType = "request_queued"
	EventRequestForwarding EventType = "request_forwarding"
	EventRequestCompleted  EventType = "request_completed"
	EventRequestFailed     EventType = "request_failed"
)

type BodyPreview struct {
	Text        string
	ContentType string
	Size        int64
	Omitted     bool
	Reason      string
}

type RequestEvent struct {
	ID              uint64
	Type            EventType
	Time            time.Time
	Method          string
	RequestURI      string
	Host            string
	RemoteAddr      string
	RequestHeader   http.Header
	ResponseHeader  http.Header
	RequestPreview  BodyPreview
	ResponsePreview BodyPreview
	StatusCode      int
	RequestSize     int64
	ResponseSize    int64
	Duration        time.Duration
	QueueDepth      int
	Error           string
}

func (e RequestEvent) RequestLine() string {
	return RequestLine(e.Method, e.RequestURI)
}

func RequestLine(method, requestURI string) string {
	if requestURI == "" {
		requestURI = "/"
	}
	return fmt.Sprintf("%s %s", method, requestURI)
}

func PublicURL(name, domain, serverAddr string) string {
	if domain == "" {
		domain = hostWithoutPort(serverAddr)
	}
	domain = strings.Trim(domain, ".")
	if domain == "" {
		return name
	}
	return "https://" + name + "." + domain
}

func hostWithoutPort(addr string) string {
	if strings.HasPrefix(addr, "[") {
		if end := strings.Index(addr, "]"); end >= 0 {
			return strings.Trim(addr[1:end], ".")
		}
	}
	if host, port, ok := strings.Cut(addr, ":"); ok && port != "" {
		return strings.Trim(host, ".")
	}
	return strings.Trim(addr, ".")
}
