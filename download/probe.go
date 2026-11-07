package download

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProbeFileSize reads response metadata through the configured download route.
// A missing result means that the server did not expose a reliable file size.
func (m *Manager) ProbeFileSize(ctx context.Context, order Request) *int64 {
	if order.MediaMode != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	order.Headers = cloneHeaders(order.Headers)
	m.applySiteLogin(&order)
	m.mu.Lock()
	headers := downloadHeaders(order.Headers, m.userAgent)
	m.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, order.URL, nil)
	if err != nil {
		return nil
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if request.Header.Get("Referer") == "" {
		request.Header.Set("Referer", sourceReferer(order.PageURL, request.URL))
	}
	request.Header.Set("Accept-Encoding", "identity")
	for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		request.Header.Del(name)
	}
	response, err := resolveDownloadResponse(request, false, m.DoResourceRequest)
	if err != nil {
		return nil
	}
	size := probeResponseSize(response)
	landing := isHTMLResponse(response)
	response.Body.Close()
	if size != nil {
		return size
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusNotImplemented {
		return nil
	}
	request = request.Clone(ctx)
	request.Method = http.MethodGet
	if !landing {
		request.Header.Set("Range", "bytes=0-0")
	}
	response, err = resolveDownloadResponse(request, false, m.DoResourceRequest)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	return probeResponseSize(response)
}

func probeResponseSize(response *http.Response) *int64 {
	mime := strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0])
	if strings.EqualFold(mime, "text/html") || strings.EqualFold(mime, "application/xhtml+xml") {
		return nil
	}
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil
	}
	if response.StatusCode == http.StatusOK && response.ContentLength >= 0 {
		size := response.ContentLength
		return &size
	}
	value := strings.TrimSpace(response.Header.Get("Content-Range"))
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable && value == "bytes */0" {
		size := int64(0)
		return &size
	}
	rangeText, sizeText, hasSize := strings.Cut(value, "/")
	if response.StatusCode != http.StatusPartialContent || !hasSize || !strings.EqualFold(rangeText, "bytes 0-0") {
		return nil
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size <= 0 || strconv.FormatInt(size, 10) != sizeText {
		return nil
	}
	return &size
}
