package client

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

const previewLimit = 4096

type bodyPreviewCapture struct {
	contentType string
	buf         bytes.Buffer
	size        int64
}

func newBodyPreviewCapture(contentType string) *bodyPreviewCapture {
	return &bodyPreviewCapture{contentType: contentType}
}

func (c *bodyPreviewCapture) Write(p []byte) (int, error) {
	n := len(p)
	c.size += int64(n)
	remaining := previewLimit - c.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = c.buf.Write(p)
	}
	return n, nil
}

func (c *bodyPreviewCapture) Preview() BodyPreview {
	preview := BodyPreview{
		ContentType: c.contentType,
		Size:        c.size,
	}
	if c.size == 0 {
		return preview
	}

	text := c.buf.String()
	if !isTextContent(c.contentType, c.buf.Bytes()) {
		preview.Omitted = true
		preview.Reason = "binary body"
		return preview
	}
	if c.size > int64(previewLimit) {
		preview.Omitted = true
		preview.Reason = "truncated"
	}
	preview.Text = text
	return preview
}

type previewReadCloser struct {
	io.ReadCloser
	capture *bodyPreviewCapture
}

func (r *previewReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		_, _ = r.capture.Write(p[:n])
	}
	return n, err
}

func wrapBodyForPreview(header http.Header, body io.ReadCloser) (*previewReadCloser, *bodyPreviewCapture) {
	capture := newBodyPreviewCapture(header.Get("Content-Type"))
	return &previewReadCloser{ReadCloser: body, capture: capture}, capture
}

func isTextContent(contentType string, sample []byte) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		if strings.HasPrefix(mediaType, "text/") {
			return true
		}
		switch mediaType {
		case "application/json", "application/xml", "application/x-www-form-urlencoded", "application/javascript":
			return true
		}
		if strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") {
			return true
		}
		return false
	}
	return utf8.Valid(sample)
}
