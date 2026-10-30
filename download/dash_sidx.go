package download

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openDL/i18n"
)

// materializeDASHIndex fetches only the chosen representation's index. A
// strong validator ties ranges to one resource version. Without one, a
// publisher checksum is required before the final output can be published.
func (m *Manager) materializeDASHIndex(t *Task, original, page *url.URL, track *dashTrack) error {
	for attempt := 0; ; attempt++ {
		err := m.materializeDASHIndexOnce(t, original, page, track)
		if err == nil {
			return nil
		}
		var readErr *responseReadError
		if !errors.As(err, &readErr) || !transientError(readErr) || attempt == maxRequestAttempts-1 {
			return err
		}
		if err := waitRetry(t.cancel, time.Duration(250*(1<<attempt))*time.Millisecond); err != nil {
			return err
		}
	}
}

func (m *Manager) materializeDASHIndexOnce(t *Task, original, page *url.URL, track *dashTrack) error {
	if track.Index == nil {
		return nil
	}
	index := track.Index
	req, cancel, err := hlsRequest(t, original, page, index.Resource)
	if err != nil {
		return err
	}
	defer cancel()
	end := index.Start + index.Length - 1
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", index.Start, end))
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || resp.Request == nil || resp.Request.URL == nil {
		return i18n.NewError("DASH 索引范围响应无效")
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return i18n.NewError("DASH 索引使用了不支持的内容编码")
	}
	prefix := fmt.Sprintf("bytes %d-%d/", index.Start, end)
	value := strings.TrimSpace(resp.Header.Get("Content-Range"))
	if !strings.HasPrefix(value, prefix) {
		return i18n.NewError("DASH 索引范围响应无效")
	}
	total, err := strconv.ParseInt(strings.TrimPrefix(value, prefix), 10, 64)
	if err != nil || total <= end || (resp.ContentLength >= 0 && resp.ContentLength != index.Length) {
		return i18n.NewError("DASH 索引范围响应无效")
	}
	validator := strongValidator(resp.Header)
	if validator == "" {
		t.metaMu.Lock()
		expected := t.expectedSHA256
		t.metaMu.Unlock()
		expected, err = NormalizeSHA256(expected)
		if err != nil {
			return err
		}
		if expected == "" {
			return i18n.NewError("DASH 索引资源缺少可靠的版本标识")
		}
	}
	resource := resourceFingerprint(resp.Request.URL)
	body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
	data, err := m.readExactMediaBody(t, body, index.Length, i18n.NewError("DASH 索引范围响应无效"))
	if err != nil {
		return err
	}
	segments, err := parseDASHSIDX(data, index.Start, total, index.Resource, validator, resource)
	if err != nil {
		return err
	}
	budget := dashIndexBudget{requests: 128, bytes: 16 << 20, visited: map[string]bool{}}
	segments, err = m.expandDASHSIDX(t, original, page, segments, 0, &budget)
	if err != nil {
		return err
	}
	var previousEnd int64
	for i, segment := range segments {
		if i > 0 && segment.start < previousEnd {
			return i18n.NewError("DASH 索引媒体范围重复或顺序无效")
		}
		previousEnd = segment.start + segment.length
	}
	init := index.Initialization
	if init.url == index.Resource {
		init.indexValidator = validator
		init.indexResource = resource
		init.indexTotal = total
	}
	track.Segments = append([]hlsSegment{init}, segments...)
	for i := range track.Segments {
		track.Segments[i].period = index.Period
		track.Segments[i].dashTimescale = index.Timescale
		track.Segments[i].dashOffset = index.Offset
		track.Segments[i].dashPeriodDuration = index.Duration
	}
	return nil
}

func parseDASHSIDX(data []byte, absoluteStart, total int64, address, validator, resource string) ([]hlsSegment, error) {
	budget := 100000
	boxes, err := mp4Boxes(bytes.NewReader(data), 0, int64(len(data)), &budget)
	if err != nil {
		return nil, err
	}
	var selected *mp4Box
	for i := range boxes {
		if boxes[i].kind == "sidx" {
			if selected != nil {
				return nil, i18n.NewError("DASH 索引包含多个 sidx")
			}
			selected = &boxes[i]
		}
	}
	if selected == nil {
		return nil, i18n.NewError("DASH 索引缺少 sidx")
	}
	box := *selected
	payload, err := mp4Read(bytes.NewReader(data), box, 0, box.end-box.body)
	if err != nil {
		return nil, err
	}
	if len(payload) < 20 {
		return nil, i18n.NewError("DASH sidx 数据不完整")
	}
	version := payload[0]
	if version > 1 || binary.BigEndian.Uint32(payload[8:12]) == 0 {
		return nil, i18n.NewError("DASH sidx 版本或时间刻度无效")
	}
	var first uint64
	var cursor int
	if version == 0 {
		first = uint64(binary.BigEndian.Uint32(payload[16:20]))
		cursor = 20
	} else {
		if len(payload) < 28 {
			return nil, i18n.NewError("DASH sidx 数据不完整")
		}
		first = binary.BigEndian.Uint64(payload[20:28])
		cursor = 28
	}
	if len(payload) < cursor+4 {
		return nil, i18n.NewError("DASH sidx 数据不完整")
	}
	count := int(binary.BigEndian.Uint16(payload[cursor+2 : cursor+4]))
	cursor += 4
	if count == 0 || count >= maxHLSSegments || len(payload)-cursor != count*12 {
		return nil, i18n.NewError("DASH sidx 分片数量或长度无效")
	}
	if absoluteStart < 0 || box.end > math.MaxInt64-absoluteStart || first > uint64(math.MaxInt64-(absoluteStart+box.end)) {
		return nil, i18n.NewError("DASH 索引字节范围超出限制")
	}
	offset := absoluteStart + box.end + int64(first)
	segments := make([]hlsSegment, 0, count)
	for i := 0; i < count; i++ {
		entry := binary.BigEndian.Uint32(payload[cursor : cursor+4])
		cursor += 12
		length := int64(entry & 0x7fffffff)
		if length == 0 || offset > total || length > total-offset {
			return nil, i18n.NewError("DASH 索引字节范围超出限制")
		}
		segments = append(segments, hlsSegment{url: address, start: offset, length: length, mp4: true, indexValidator: validator, indexResource: resource, indexTotal: total, indexNested: entry>>31 != 0})
		offset += length
	}
	return segments, nil
}

type dashIndexBudget struct {
	requests int
	bytes    int64
	visited  map[string]bool
}

// Nested references are bounded and read under If-Range using the initial
// response's validator. A changing index never supplies media byte ranges.
func (m *Manager) expandDASHSIDX(t *Task, original, page *url.URL, refs []hlsSegment, depth int, budget *dashIndexBudget) ([]hlsSegment, error) {
	if depth > 4 {
		return nil, i18n.NewError("DASH 嵌套索引层级超过限制")
	}
	var result []hlsSegment
	for _, ref := range refs {
		if !ref.indexNested {
			result = append(result, ref)
			if len(result) >= maxHLSSegments {
				return nil, i18n.NewError("DASH 分片数量超过限制")
			}
			continue
		}
		if ref.length > 4<<20 || ref.length > budget.bytes || budget.requests <= 0 {
			return nil, i18n.NewError("DASH 嵌套索引超过资源限制")
		}
		key := fmt.Sprintf("%d:%d", ref.start, ref.length)
		if budget.visited[key] {
			return nil, i18n.NewError("DASH 嵌套索引包含循环或重复引用")
		}
		budget.visited[key] = true
		budget.requests--
		budget.bytes -= ref.length
		req, cancel, err := hlsRequest(t, original, page, ref.url)
		if err != nil {
			return nil, err
		}
		end := ref.start + ref.length - 1
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", ref.start, end))
		if ref.indexValidator != "" {
			req.Header.Set("If-Range", ref.indexValidator)
		}
		resp, err := m.doWithRetry(t, req)
		if err != nil {
			cancel()
			return nil, err
		}
		want := fmt.Sprintf("bytes %d-%d/%d", ref.start, end, ref.indexTotal)
		if resp.StatusCode != http.StatusPartialContent || resp.Request == nil || resp.Request.URL == nil || resourceFingerprint(resp.Request.URL) != ref.indexResource || (ref.indexValidator != "" && strongValidator(resp.Header) != ref.indexValidator) || strings.TrimSpace(resp.Header.Get("Content-Range")) != want || (resp.ContentLength >= 0 && resp.ContentLength != ref.length) || (resp.Header.Get("Content-Encoding") != "" && !strings.EqualFold(resp.Header.Get("Content-Encoding"), "identity")) {
			resp.Body.Close()
			cancel()
			return nil, i18n.NewError("DASH 索引对应的资源版本发生变化")
		}
		body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
		data, readErr := m.readExactMediaBody(t, body, ref.length, i18n.NewError("DASH 索引范围响应无效"))
		resp.Body.Close()
		cancel()
		if readErr != nil {
			return nil, readErr
		}
		children, err := parseDASHSIDX(data, ref.start, ref.indexTotal, ref.url, ref.indexValidator, ref.indexResource)
		if err != nil {
			return nil, err
		}
		children, err = m.expandDASHSIDX(t, original, page, children, depth+1, budget)
		if err != nil {
			return nil, err
		}
		if len(result)+len(children) >= maxHLSSegments {
			return nil, i18n.NewError("DASH 分片数量超过限制")
		}
		result = append(result, children...)
	}
	return result, nil
}
