package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gatelet/internal/client"
)

func (m model) renderInspector(width, height int) string {
	item, ok := m.selectedRequest()
	if !ok {
		return fitBlock(rowStyle.Render(mutedStyle.Render("No request selected.")), width, height)
	}

	content := formatInspector(item, width, m.now, m.plainBody, m.inspectorTab)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	visible := max(1, height)
	start := min(max(0, m.detailScroll), max(0, len(lines)-visible))
	end := min(len(lines), start+visible)

	var b strings.Builder
	for _, line := range lines[start:end] {
		b.WriteString(visibleWindow(line, 0, width))
		b.WriteString("\n")
	}
	if len(lines) > visible && end <= len(lines) {
		progress := fmt.Sprintf("lines %d-%d of %d", start+1, end, len(lines))
		b.WriteString(rowStyle.Render(mutedStyle.Render(progress)))
	}
	return fitBlock(b.String(), width, height)
}

func (m model) renderBody(width, height int) string {
	item, ok := m.selectedRequest()
	if !ok {
		return fitBlock(rowStyle.Render(mutedStyle.Render("No request selected.")), width, height)
	}

	lines := bodyViewLines(item, width, m.plainBody, m.inspectorTab)
	visible := max(1, height)
	start := min(max(0, m.bodyScroll), max(0, len(lines)-visible))
	end := min(len(lines), start+visible)

	var b strings.Builder
	for _, line := range lines[start:end] {
		b.WriteString(padRight(line, width))
		b.WriteString("\n")
	}
	if len(lines) > visible && end <= len(lines) {
		progress := fmt.Sprintf("lines %d-%d of %d", start+1, end, len(lines))
		b.WriteString(rowStyle.Render(mutedStyle.Render(progress)))
	}
	return fitBlock(b.String(), width, height)
}

func formatBodyView(item requestItem, width int, plainBody bool, tab inspectorTab) string {
	var b strings.Builder
	if tab == inspectorTabResponse {
		writePreview(&b, "BODY", item.ResponsePreview, width, plainBody, bodyStateResponse(item), 0)
	} else {
		writePreview(&b, "BODY", item.RequestPreview, width, plainBody, "", 0)
	}
	return strings.TrimRight(strings.TrimPrefix(b.String(), "\n"), "\n")
}

func bodyViewLines(item requestItem, width int, plainBody bool, tab inspectorTab) []string {
	content := formatBodyView(item, width, plainBody, tab)
	content = wrapVisibleBlock(content, width)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func formatInspector(item requestItem, width int, now time.Time, plainBody bool, tab inspectorTab) string {
	if tab == inspectorTabResponse {
		return formatResponseInspector(item, width, plainBody)
	}
	return formatRequestInspector(item, width, now, plainBody)
}

func formatRequestInspector(item requestItem, width int, now time.Time, plainBody bool) string {
	if now.IsZero() {
		now = time.Now()
	}
	if item.Method == client.MethodTCP {
		return formatTCPInspector(item, now)
	}
	var b strings.Builder
	b.WriteString(rowStyle.Render(headStyle.Render("REQUEST")))
	b.WriteString("\n")
	writeMeta(&b, "URL", item.Method+" "+item.RequestURI)
	if item.TargetURL != "" {
		writeMeta(&b, "Forwarded to", item.TargetURL)
	}
	writeMeta(&b, "State", stateLabel(item.State))
	writeMeta(&b, "Client", remoteIP(item.RemoteAddr))
	writeMeta(&b, "Host", item.Host)
	writeMeta(&b, "Started", item.StartedAt.Format("2006-01-02 15:04:05"))
	writeMeta(&b, "Age", relativeAge(now, item.StartedAt))
	writeMeta(&b, "Request Size", formatBytes(item.RequestSize))
	if item.ErrorKind != "" {
		writeMeta(&b, "Error Kind", errorKindLabel(item.ErrorKind))
	}
	if item.Error != "" {
		writeMeta(&b, "Error", status5xxStyle.Render(item.Error))
	}

	b.WriteString("\n")
	b.WriteString(rowStyle.Render(headStyle.Render("REQUEST HEADERS")))
	b.WriteString("\n")
	writeHeaders(&b, item.RequestHeader, 20)
	writePreview(&b, "BODY", item.RequestPreview, width, plainBody, "", 500)
	return strings.TrimRight(b.String(), "\n")
}

func formatTCPInspector(item requestItem, now time.Time) string {
	var b strings.Builder
	b.WriteString(rowStyle.Render(headStyle.Render("TCP CONNECTION")))
	b.WriteString("\n")
	writeMeta(&b, "Remote", remoteIP(item.RemoteAddr))
	if item.TargetURL != "" {
		writeMeta(&b, "Forwarded to", item.TargetURL)
	}
	writeMeta(&b, "State", stateLabel(item.State))
	writeMeta(&b, "Started", item.StartedAt.Format("2006-01-02 15:04:05"))
	writeMeta(&b, "Age", relativeAge(now, item.StartedAt))
	writeMeta(&b, "Bytes In", formatBytes(item.RequestSize))
	writeMeta(&b, "Bytes Out", formatBytes(item.ResponseSize))
	if item.Duration > 0 {
		writeMeta(&b, "Duration", item.Duration.Round(time.Millisecond).String())
	}
	if item.ErrorKind != "" {
		writeMeta(&b, "Error Kind", errorKindLabel(item.ErrorKind))
	}
	if item.Error != "" {
		writeMeta(&b, "Error", status5xxStyle.Render(item.Error))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatResponseInspector(item requestItem, width int, plainBody bool) string {
	var b strings.Builder
	b.WriteString(rowStyle.Render(headStyle.Render("RESPONSE")))
	b.WriteString("\n")
	writeMeta(&b, "URL", item.Method+" "+item.RequestURI)
	writeMeta(&b, "Status", styledStatus(item))
	writeMeta(&b, "State", stateLabel(item.State))
	if item.TargetURL != "" {
		writeMeta(&b, "Target", item.TargetURL)
	}
	writeMeta(&b, "Timing", fmt.Sprintf("Upstream %s", item.Duration.Round(time.Millisecond)))
	writeMeta(&b, "Request Size", formatBytes(item.RequestSize))
	writeMeta(&b, "Response Size", formatBytes(item.ResponseSize))
	if item.ErrorKind != "" {
		writeMeta(&b, "Error Kind", errorKindLabel(item.ErrorKind))
	}
	if item.Error != "" {
		writeMeta(&b, "Error", status5xxStyle.Render(item.Error))
	}

	b.WriteString("\n")
	b.WriteString(rowStyle.Render(headStyle.Render("RESPONSE HEADERS")))
	b.WriteString("\n")
	writeHeaders(&b, item.ResponseHeader, 20)
	writePreview(&b, "BODY", item.ResponsePreview, width, plainBody, bodyStateResponse(item), 500)
	return strings.TrimRight(b.String(), "\n")
}

func errorKindLabel(kind client.ErrorKind) string {
	switch kind {
	case client.ErrorKindLocalTarget:
		return status5xxStyle.Render("local target")
	case client.ErrorKindTunnel:
		return queuedStyle.Render("tunnel")
	default:
		return valStyle.Render(string(kind))
	}
}

func (m model) detailHeight() int {
	if m.height <= 0 {
		return 24
	}
	return max(1, m.height-2)
}

func (m model) bodyHeight() int {
	if m.height <= 0 {
		return 24
	}
	return max(1, m.height-2)
}

func writeMeta(b *strings.Builder, label, value string) {
	b.WriteString("  ")
	b.WriteString(keyStyle.Render(label + ":"))
	b.WriteString(" ")
	b.WriteString(valStyle.Render(value))
	b.WriteString("\n")
}

func writeHeaders(b *strings.Builder, header map[string][]string, limit int) {
	if len(header) == 0 {
		b.WriteString("  ")
		b.WriteString(mutedStyle.Render("(none)"))
		b.WriteString("\n")
		return
	}
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		if i >= limit {
			fmt.Fprintf(b, "  %s\n", mutedStyle.Render(fmt.Sprintf("... %d more", len(keys)-limit)))
			return
		}
		b.WriteString("  ")
		b.WriteString(keyStyle.Render(key + ":"))
		b.WriteString(" ")
		b.WriteString(valStyle.Render(strings.Join(header[key], ", ")))
		b.WriteString("\n")
	}
}

func writePreview(b *strings.Builder, title string, preview client.BodyPreview, width int, plain bool, state string, limit int) {
	b.WriteString("\n")
	b.WriteString(rowStyle.Render(headStyle.Render(title)))
	b.WriteString("\n")
	if state != "" && preview.Size == 0 && preview.Text == "" && !preview.Omitted {
		b.WriteString("  ")
		b.WriteString(mutedStyle.Render(state))
		b.WriteString("\n")
		return
	}
	if preview.Size == 0 {
		b.WriteString("  ")
		b.WriteString(mutedStyle.Render("(empty)"))
		b.WriteString("\n")
		return
	}
	if preview.Omitted && preview.Text == "" {
		fmt.Fprintf(b, "  %s\n", mutedStyle.Render("omitted: "+preview.Reason))
		if preview.ContentType != "" {
			fmt.Fprintf(b, "  %s\n", mutedStyle.Render("content-type: "+preview.ContentType))
		}
		fmt.Fprintf(b, "  %s\n", mutedStyle.Render(capturedSummary(preview)))
		return
	}
	text := previewText(preview, plain, limit)
	text = strings.ReplaceAll(text, "\n", "\n  ")
	fmt.Fprintf(b, "  %s\n", text)
	if preview.Omitted {
		fmt.Fprintf(b, "  %s\n", mutedStyle.Render(fmt.Sprintf("omitted: %s; %s", preview.Reason, capturedSummary(preview))))
	}
}

func bodyStateResponse(item requestItem) string {
	switch item.State {
	case client.EventResponseStarted:
		return "(streaming response; body preview will appear when data is captured)"
	case client.EventRequestForwarding:
		return "(waiting for upstream response)"
	default:
		return ""
	}
}

func capturedSummary(preview client.BodyPreview) string {
	captured := preview.Captured
	if captured == 0 && preview.Text != "" {
		captured = int64(len(preview.Text))
	}
	if captured == 0 && preview.Size > 0 && !preview.Omitted {
		captured = preview.Size
	}
	if preview.Size > 0 {
		return fmt.Sprintf("captured %s of %s", formatBytes(captured), formatBytes(preview.Size))
	}
	return fmt.Sprintf("captured %s", formatBytes(captured))
}

func previewText(preview client.BodyPreview, plain bool, limit int) string {
	if plain {
		return valStyle.Render(limitPreviewText(preview.Text, limit))
	}
	formatted, ok := formatJSON(preview.Text)
	if !ok || !canFormatJSON(preview) {
		return valStyle.Render(limitPreviewText(preview.Text, limit))
	}
	return colorizeJSON(limitPreviewText(formatted, limit))
}

func limitPreviewText(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	return truncate(text, limit)
}

func canFormatJSON(preview client.BodyPreview) bool {
	if strings.TrimSpace(preview.Text) == "" {
		return false
	}
	if isJSONContentType(preview.ContentType) {
		return true
	}
	_, ok := formatJSON(preview.Text)
	return ok
}

func isJSONContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func formatJSON(text string) (string, bool) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(text), "", "  "); err != nil {
		return "", false
	}
	return buf.String(), true
}

func colorizeJSON(text string) string {
	var b strings.Builder
	inString := false
	escaped := false
	var token strings.Builder

	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inString {
			token.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
				s := token.String()
				token.Reset()
				if nextNonSpace(text, i+1) == ':' {
					b.WriteString(keyStyle.Render(s))
				} else {
					b.WriteString(valStyle.Render(s))
				}
			}
			continue
		}

		switch ch {
		case '"':
			flushJSONToken(&b, &token)
			inString = true
			token.WriteByte(ch)
		case '{', '}', '[', ']', ':', ',':
			flushJSONToken(&b, &token)
			b.WriteByte(ch)
		case ' ', '\n', '\t', '\r':
			flushJSONToken(&b, &token)
			b.WriteByte(ch)
		default:
			token.WriteByte(ch)
		}
	}
	flushJSONToken(&b, &token)
	return b.String()
}

func flushJSONToken(b *strings.Builder, token *strings.Builder) {
	if token.Len() == 0 {
		return
	}
	text := token.String()
	token.Reset()
	switch text {
	case "true", "false", "null":
		b.WriteString(status3xxStyle.Render(text))
	default:
		b.WriteString(queuedStyle.Render(text))
	}
}

func nextNonSpace(text string, start int) byte {
	for i := start; i < len(text); i++ {
		switch text[i] {
		case ' ', '\n', '\t', '\r':
			continue
		default:
			return text[i]
		}
	}
	return 0
}
