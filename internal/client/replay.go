package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

func CurlCommand(event RequestEvent, publicURL string) (string, error) {
	body, hasBody, err := replayBody(event)
	if err != nil {
		return "", err
	}

	var parts []string
	parts = append(parts, "curl", "-X", emptyDefault(event.Method, http.MethodGet))
	parts = append(parts, shellQuote(publicRequestURL(publicURL, event.RequestURI)))

	keys := make([]string, 0, len(event.RequestHeader))
	for key := range event.RequestHeader {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, value := range event.RequestHeader[key] {
			parts = append(parts, "-H", shellQuote(key+": "+value))
		}
	}
	if hasBody {
		parts = append(parts, "--data-binary", shellQuote(string(body)))
	}
	return strings.Join(parts, " "), nil
}

func ReplayRequest(ctx context.Context, target string, event RequestEvent) (RequestEvent, error) {
	id := atomic.AddUint64(&requestID, 1)
	started := time.Now()
	result := RequestEvent{
		ID:            id,
		Type:          EventRequestFailed,
		Time:          started,
		Method:        event.Method,
		RequestURI:    event.RequestURI,
		Host:          event.Host,
		RemoteAddr:    "local replay",
		RequestHeader: cloneHeader(event.RequestHeader),
		RequestSize:   event.RequestPreview.Size,
	}

	body, _, err := replayBody(event)
	if err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started)
		return result, err
	}
	result.RequestPreview = BodyPreview{
		Text:        string(body),
		ContentType: event.RequestPreview.ContentType,
		Size:        event.RequestPreview.Size,
	}

	targetURL, err := targetRequestURL(target, event.RequestURI)
	if err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started)
		return result, err
	}

	req, err := http.NewRequestWithContext(ctx, emptyDefault(event.Method, http.MethodGet), targetURL, bytes.NewReader(body))
	if err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started)
		return result, err
	}
	copyHeader(req.Header, event.RequestHeader)
	req.Host = event.Host

	resp, err := localHTTPClient.Do(req)
	if err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started)
		return result, err
	}
	defer resp.Body.Close()

	respBody, respPreview := wrapBodyForPreview(resp.Header, resp.Body, DefaultPreviewLimit)
	resp.Body = respBody
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started)
		return result, err
	}

	responsePreview := respPreview.Preview()
	result.Type = EventRequestCompleted
	result.Time = time.Now()
	result.ResponseHeader = cloneHeader(resp.Header)
	result.ResponsePreview = responsePreview
	result.StatusCode = resp.StatusCode
	result.ResponseSize = responsePreview.Size
	result.Duration = time.Since(started)
	return result, nil
}

func replayBody(event RequestEvent) ([]byte, bool, error) {
	if event.RequestPreview.Omitted {
		return nil, false, fmt.Errorf("request body is unavailable: %s", event.RequestPreview.Reason)
	}
	if event.RequestPreview.Size == 0 {
		return nil, false, nil
	}
	body := []byte(event.RequestPreview.Text)
	if int64(len(body)) != event.RequestPreview.Size {
		return nil, false, fmt.Errorf("request body preview is incomplete")
	}
	return body, true, nil
}

func publicRequestURL(publicURL, requestURI string) string {
	publicURL = strings.TrimRight(publicURL, "/")
	if requestURI == "" {
		requestURI = "/"
	}
	if !strings.HasPrefix(requestURI, "/") {
		requestURI = "/" + requestURI
	}
	return publicURL + requestURI
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
