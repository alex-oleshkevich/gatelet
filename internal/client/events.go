package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultPauseTimeout = 60 * time.Second

type LogFormat string

const (
	LogFormatText  LogFormat = "text"
	LogFormatJSON  LogFormat = "json"
	LogFormatJSONL LogFormat = "jsonl"
)

type EventType string

const (
	EventRequestReceived   EventType = "request_received"
	EventRequestQueued     EventType = "request_queued"
	EventRequestForwarding EventType = "request_forwarding"
	EventRequestCompleted  EventType = "request_completed"
	EventRequestFailed     EventType = "request_failed"
)

type ErrorKind string

const (
	ErrorKindLocalTarget ErrorKind = "local_target"
	ErrorKindTunnel      ErrorKind = "tunnel"
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
	ErrorKind       ErrorKind
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

func LogLine(event RequestEvent) string {
	status := "-"
	if event.Error != "" {
		status = "ERR"
	} else if event.StatusCode != 0 {
		status = fmt.Sprint(event.StatusCode)
	}
	return fmt.Sprintf("%s %s %s %s %s",
		emptyDefault(event.Method, "-"),
		emptyDefault(event.RequestURI, "/"),
		status,
		FormatBytes(event.RequestSize),
		remoteIP(event.RemoteAddr),
	)
}

func ParseLogFormat(value string) (LogFormat, error) {
	switch LogFormat(value) {
	case "", LogFormatText:
		return LogFormatText, nil
	case LogFormatJSON:
		return LogFormatJSON, nil
	case LogFormatJSONL:
		return LogFormatJSONL, nil
	default:
		return "", fmt.Errorf("unsupported log format %q", value)
	}
}

func RequestLogLine(event RequestEvent, format LogFormat) (string, error) {
	format, err := ParseLogFormat(string(format))
	if err != nil {
		return "", err
	}
	if format == LogFormatText {
		return LogLine(event), nil
	}

	record := requestLogRecord{
		Type:        "request",
		Method:      emptyDefault(event.Method, "-"),
		Path:        emptyDefault(event.RequestURI, "/"),
		Status:      event.StatusCode,
		RequestSize: event.RequestSize,
		RemoteIP:    remoteIP(event.RemoteAddr),
		DurationMS:  float64(event.Duration.Microseconds()) / 1000,
		Error:       event.Error,
		ErrorKind:   event.ErrorKind,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type requestLogRecord struct {
	Type        string    `json:"type"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Status      int       `json:"status"`
	RequestSize int64     `json:"request_size"`
	RemoteIP    string    `json:"remote_ip"`
	DurationMS  float64   `json:"duration_ms"`
	Error       string    `json:"error,omitempty"`
	ErrorKind   ErrorKind `json:"error_kind,omitempty"`
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

func FormatBytes(size int64) string {
	if size < 0 {
		size = 0
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}
	value := float64(size)
	units := []string{"kb", "mb", "gb"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			if value >= 10 {
				return fmt.Sprintf("%.0f%s", value, suffix)
			}
			return fmt.Sprintf("%.1f%s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1ftb", value/unit)
}

func hostWithoutPort(addr string) string {
	if parsed, err := url.Parse(addr); err == nil && parsed.Host != "" {
		addr = parsed.Host
	}
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

func remoteIP(remoteAddr string) string {
	host, _, ok := strings.Cut(remoteAddr, ":")
	if ok && host != "" {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(remoteAddr, "[]")
}

func emptyDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
