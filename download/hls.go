package download

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openDL/pathrec"

	"openDL/i18n"
)

const maxHLSPlaylist = 2 << 20
const maxHLSSegments = 10000
const maxMediaPeriods = 64

var errHLSLiveInitSwitch = i18n.NewError("暂不支持直播期间切换 HLS 初始化段")

type hlsSegment struct {
	url                           string
	start, length                 int64 // zero length means the entire resource
	mp4, init                     bool
	period                        int           // 多周期 DASH 清单的周期序号，从 0 开始
	dashTimescale, dashOffset     uint64        // DASH 时间刻度与 presentationTimeOffset
	dashPeriodDuration            time.Duration // zero if the final period is unbounded
	hlsDiscontinuity              uint64        // HLS discontinuity sequence for this segment
	keyURL                        string        // AES-128 密钥地址；空表示不加密
	iv                            []byte        // 16 字节解密向量
	seq                           int64         // 媒体序号，用于默认解密向量
	liveSequence                  int64
	hasLiveSequence               bool
	indexValidator, indexResource string
	indexTotal                    int64
	indexNested                   bool // transient sidx reference; never a media request
}

// equalHLSSegment 报告两个分片是否描述同一段数据；hlsSegment 含 []byte，
// 不能直接比较。
func equalHLSSegment(a, b hlsSegment) bool {
	return a.url == b.url && a.start == b.start && a.length == b.length &&
		a.mp4 == b.mp4 && a.init == b.init && a.keyURL == b.keyURL &&
		a.seq == b.seq && bytes.Equal(a.iv, b.iv)
}

// hlsSegmentIV 返回分片的解密向量：显式 IV 优先，否则按媒体序号推导。
func hlsSegmentIV(override []byte, seq int64) []byte {
	if len(override) == 16 {
		return append([]byte(nil), override...)
	}
	iv := make([]byte, 16)
	binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	return iv
}

// readHLSAES 读取并解密一个 AES-128 分片，校验并去掉 PKCS#7 填充。
func readHLSAES(source io.Reader, key, iv []byte) ([]byte, error) {
	return readHLSAESWithLength(source, key, iv, -1)
}

func readHLSAESWithLength(source io.Reader, key, iv []byte, expected int64) ([]byte, error) {
	const maxHLSCipherSegment = 64 << 20
	if expected > maxHLSCipherSegment {
		return nil, fmt.Errorf(i18n.T("暂不支持超过 64 MiB 的加密分片"))
	}
	cipherText, err := io.ReadAll(io.LimitReader(source, maxHLSCipherSegment+1))
	if err != nil {
		return nil, &responseReadError{err: err}
	}
	if len(cipherText) == 0 || len(cipherText) > maxHLSCipherSegment {
		if len(cipherText) == 0 && expected > 0 && expected <= maxHLSCipherSegment {
			return nil, &responseReadError{err: io.ErrUnexpectedEOF}
		}
		return nil, fmt.Errorf(i18n.T("暂不支持超过 64 MiB 的加密分片"))
	}
	if expected >= 0 && int64(len(cipherText)) != expected {
		return nil, &responseReadError{err: io.ErrUnexpectedEOF}
	}
	if len(cipherText)%aes.BlockSize != 0 {
		return nil, fmt.Errorf(i18n.T("HLS 加密分片长度无效"))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("HLS 密钥无效"))
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf(i18n.T("HLS 解密向量无效"))
	}
	plain := make([]byte, len(cipherText))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, cipherText)
	padding := int(plain[len(plain)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(plain) {
		return nil, fmt.Errorf(i18n.T("HLS 解密填充无效"))
	}
	for _, b := range plain[len(plain)-padding:] {
		if int(b) != padding {
			return nil, fmt.Errorf(i18n.T("HLS 解密填充无效"))
		}
	}
	return plain[:len(plain)-padding], nil
}

// hlsKey 返回媒体密钥；同一次任务内按密钥地址缓存。
func (m *Manager) hlsKey(t *Task, original, page *url.URL, address string) ([]byte, error) {
	t.hlsKeyMu.Lock()
	if key, ok := t.hlsKeys[address]; ok {
		t.hlsKeyMu.Unlock()
		return key, nil
	}
	t.hlsKeyMu.Unlock()
	req, cancel, err := hlsRequest(t, original, page, address)
	if err != nil {
		return nil, err
	}
	defer cancel()
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(i18n.T("无法读取 HLS 密钥（HTTP %d）"), resp.StatusCode)
	}
	if resp.ContentLength > 64 {
		return nil, fmt.Errorf(i18n.T("HLS 密钥长度无效"))
	}
	body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
	var data []byte
	if resp.ContentLength >= 0 {
		data, err = m.readExactMediaBody(t, body, resp.ContentLength, i18n.NewError("HLS 密钥长度无效"))
	} else {
		data, err = io.ReadAll(io.LimitReader(mediaNetworkReader{manager: m, task: t, source: body}, 65))
	}
	if err != nil {
		return nil, &responseReadError{err: err}
	}
	if len(data) != aes.BlockSize {
		return nil, fmt.Errorf(i18n.T("HLS 密钥长度无效"))
	}
	t.hlsKeyMu.Lock()
	if t.hlsKeys == nil {
		t.hlsKeys = map[string][]byte{}
	}
	t.hlsKeys[address] = data
	t.hlsKeyMu.Unlock()
	return data, nil
}

type hlsRangeState struct {
	validator string
	total     int64
	resource  string
}

// mediaNetworkReader accounts for network data buffered before writing, such
// as encrypted segments and DASH indexes.
type mediaNetworkReader struct {
	manager  *Manager
	task     *Task
	source   io.Reader
	canceled <-chan struct{} // defaults to task cancellation
}

func (r mediaNetworkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	canceled := r.canceled
	if canceled == nil {
		canceled = r.task.cancel
	}
	if err := r.manager.quota.wait(canceled); err != nil {
		return 0, err
	}
	allowed, refund, err := r.manager.acquirePaced(r.task, len(p), canceled)
	if err != nil {
		return 0, err
	}
	n, err := r.source.Read(p[:allowed])
	refund(n)
	if n > 0 {
		r.task.transferred.Add(int64(n))
		r.manager.quota.add(int64(n))
	}
	return n, err
}

// The limit sits outside the meter so a full quota does not block merely to
// observe EOF after the advertised byte count. Read EOF separately to process
// response trailers and detect a body longer than declared.
func (m *Manager) readExactMediaBody(t *Task, body io.Reader, length int64, invalid error) ([]byte, error) {
	return m.readExactMediaBodyWithCancel(t, body, length, invalid, t.cancel)
}

func (m *Manager) readExactMediaBodyWithCancel(t *Task, body io.Reader, length int64, invalid error, canceled <-chan struct{}) ([]byte, error) {
	network := mediaNetworkReader{manager: m, task: t, source: body, canceled: canceled}
	data, err := io.ReadAll(io.LimitReader(network, length))
	if err != nil {
		return nil, &responseReadError{err: err}
	}
	if int64(len(data)) != length {
		return nil, &responseReadError{err: io.ErrUnexpectedEOF}
	}
	var extra [1]byte
	n, err := body.Read(extra[:])
	if n != 0 {
		return nil, invalid
	}
	if err != io.EOF {
		if err == nil {
			err = io.ErrNoProgress
		}
		return nil, &responseReadError{err: err}
	}
	return data, nil
}

func writeHLSPlaintext(t *Task, target io.Writer, plain []byte) (int64, error) {
	var total int64
	for len(plain) > 0 {
		select {
		case <-t.cancel:
			return total, contextCanceled
		default:
		}
		length := min(len(plain), 128<<10)
		if err := checkOutputGrowth(t, length); err != nil {
			return total, err
		}
		n, err := target.Write(plain[:length])
		if n > 0 {
			t.done.Add(int64(n))
			total += int64(n)
		}
		if err != nil {
			return total, err
		}
		if n != length {
			return total, io.ErrShortWrite
		}
		plain = plain[n:]
	}
	return total, nil
}

// parseHLSTS accepts finite TS and fragmented MP4 playlists, including
// AES-128 encryption and TS discontinuities. Unsupported constructs fail
// before publication.
func parseHLSTS(data []byte, base *url.URL) ([]hlsSegment, error) {
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 4096), maxHLSPlaylist)
	if !scan.Scan() || strings.TrimSpace(scan.Text()) != "#EXTM3U" {
		return nil, fmt.Errorf(i18n.T("不是有效的 HLS 播放清单"))
	}
	var segments []hlsSegment
	resourceModes := make(map[string]bool)
	var pending, ended bool
	var rangeLength, rangeStart int64
	var hasRange, explicitStart bool
	var initialization *hlsSegment
	period := 0
	boundary := false
	var keyURL string
	var keyIV []byte
	otherKeyMethods := make(map[string]string)
	checkActiveKeys := func() error {
		if len(otherKeyMethods) == 0 {
			return nil
		}
		if keyURL == "" {
			return i18n.NewError("暂不支持的 HLS 密钥格式")
		}
		for _, method := range otherKeyMethods {
			if method != "AES-128" {
				return i18n.NewError("暂不支持的 HLS 加密方式")
			}
		}
		return nil
	}
	var mediaSeq, appended int64
	var iframeOnly, encrypted bool
	sequenceDeclared := false
	var discontinuity uint64
	var discontinuityDeclared, discontinuitySeen bool
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		if ended && !strings.HasPrefix(line, "#") {
			return nil, fmt.Errorf(i18n.T("HLS 结束标签之后存在媒体分片"))
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			if pending {
				return nil, fmt.Errorf(i18n.T("HLS 分片描述格式无效"))
			}
			value, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			duration, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 {
				return nil, fmt.Errorf(i18n.T("HLS 分片时长无效"))
			}
			pending = true
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-") {
			key, value, _ := strings.Cut(line, ":")
			switch key {
			case "#EXT-X-I-FRAMES-ONLY":
				iframeOnly = true
			case "#EXT-X-ENDLIST":
				ended = true
			case "#EXT-X-KEY":
				attrs, err := hlsAttributes(value)
				if err != nil {
					return nil, err
				}
				format := attrs["KEYFORMAT"]
				if format != "" && format != "identity" {
					if attrs["METHOD"] == "" || attrs["METHOD"] == "NONE" || attrs["URI"] == "" {
						return nil, i18n.NewError("HLS 加密缺少密钥地址")
					}
					otherKeyMethods[format] = attrs["METHOD"]
					continue
				}
				switch attrs["METHOD"] {
				case "NONE":
					if len(attrs) != 1 {
						return nil, i18n.NewError("HLS 属性缺失或重复")
					}
					keyURL, keyIV = "", nil
					clear(otherKeyMethods)
				case "AES-128":
					encrypted = true
					if versions := attrs["KEYFORMATVERSIONS"]; versions != "" {
						supported := false
						for _, version := range strings.Split(versions, "/") {
							n, err := strconv.ParseUint(version, 10, 32)
							if err != nil || n == 0 || strconv.FormatUint(n, 10) != version {
								return nil, i18n.NewError("暂不支持的 HLS 密钥格式版本")
							}
							supported = supported || n == 1
						}
						if !supported {
							return nil, i18n.NewError("暂不支持的 HLS 密钥格式版本")
						}
					}
					if attrs["URI"] == "" {
						return nil, fmt.Errorf(i18n.T("HLS 加密缺少密钥地址"))
					}
					ref, err := url.Parse(attrs["URI"])
					if err != nil {
						return nil, fmt.Errorf(i18n.T("HLS 密钥地址无效"))
					}
					resolved := base.ResolveReference(ref)
					resolved.Fragment = ""
					if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Hostname() == "" {
						return nil, fmt.Errorf(i18n.T("HLS 密钥地址只支持 HTTP 或 HTTPS"))
					}
					keyURL = resolved.String()
					keyIV = nil
					if raw := attrs["IV"]; raw != "" {
						text := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "0x")
						if len(text) == 0 || len(text) > aes.BlockSize*2 {
							return nil, fmt.Errorf(i18n.T("HLS 解密向量无效"))
						}
						// HLS hexadecimal integers can omit leading zeroes.
						text = strings.Repeat("0", aes.BlockSize*2-len(text)) + text
						decoded, err := hex.DecodeString(text)
						if err != nil || len(decoded) != aes.BlockSize {
							return nil, fmt.Errorf(i18n.T("HLS 解密向量无效"))
						}
						keyIV = decoded
					}
				default:
					return nil, fmt.Errorf(i18n.T("暂不支持的 HLS 加密方式"))
				}
			case "#EXT-X-DISCONTINUITY":
				if discontinuity == math.MaxUint64 {
					return nil, i18n.NewError("HLS 不连续序号超出限制")
				}
				discontinuity++
				discontinuitySeen = true
				if initialization != nil && appended > 0 {
					if pending || hasRange || boundary || len(segments) == 0 || segments[len(segments)-1].init {
						return nil, i18n.NewError("HLS 初始化段切换位置无效")
					}
					if period >= maxMediaPeriods-1 {
						return nil, i18n.NewError("HLS 周期数量超过限制")
					}
					period++
					boundary = true
				}
			case "#EXT-X-MAP":
				if err := checkActiveKeys(); err != nil {
					return nil, err
				}
				segment, err := parseHLSMap(value, base)
				if err != nil {
					return nil, err
				}
				if keyURL != "" {
					if len(keyIV) != aes.BlockSize {
						return nil, fmt.Errorf(i18n.T("加密的 HLS 初始化段需要明确的 IV"))
					}
					segment.keyURL = keyURL
					segment.iv = append([]byte(nil), keyIV...)
				}
				if initialization != nil {
					if boundary {
						if ranged, exists := resourceModes[segment.url]; exists && ranged != (segment.length > 0) {
							return nil, i18n.NewError("暂不支持同一 HLS 资源混合完整分片与字节范围")
						}
						resourceModes[segment.url] = segment.length > 0
						initialization = &segment
						continue
					}
					if equalHLSSegment(segment, *initialization) {
						continue
					}
					if pending || hasRange || len(segments) == 0 || segments[len(segments)-1].init {
						return nil, i18n.NewError("HLS 初始化段切换位置无效")
					}
					if ranged, exists := resourceModes[segment.url]; exists && ranged != (segment.length > 0) {
						return nil, i18n.NewError("暂不支持同一 HLS 资源混合完整分片与字节范围")
					}
					resourceModes[segment.url] = segment.length > 0
					if period >= maxMediaPeriods-1 {
						return nil, i18n.NewError("HLS 周期数量超过限制")
					}
					period++
					segment.period = period
					initialization = &segment
					segments = append(segments, segment)
					if len(segments) > maxHLSSegments {
						return nil, i18n.NewError("HLS 分片数量超过限制")
					}
					continue
				}
				if len(segments) != 0 {
					return nil, fmt.Errorf(i18n.T("HLS 初始化段必须位于媒体分片之前"))
				}
				initialization = &segment
				resourceModes[segment.url] = segment.length > 0
				segments = append(segments, segment)
			case "#EXT-X-BYTERANGE":
				if hasRange {
					return nil, fmt.Errorf(i18n.T("HLS 分片范围重复"))
				}
				lengthText, startText, hasStart := strings.Cut(value, "@")
				var err error
				rangeLength, err = strconv.ParseInt(lengthText, 10, 64)
				if err != nil || rangeLength <= 0 {
					return nil, fmt.Errorf(i18n.T("HLS 分片范围长度无效"))
				}
				rangeStart, explicitStart = 0, hasStart
				if hasStart {
					rangeStart, err = strconv.ParseInt(startText, 10, 64)
					if err != nil || rangeStart < 0 {
						return nil, fmt.Errorf(i18n.T("HLS 分片范围起点无效"))
					}
				}
				hasRange = true
			// These describe the playlist or its timeline; they do not change
			// the bytes to fetch or how supported segments are concatenated.
			case "#EXT-X-MEDIA-SEQUENCE":
				if sequenceDeclared || appended > 0 {
					return nil, fmt.Errorf(i18n.T("HLS 媒体序号位置或数量无效"))
				}
				parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if err != nil || parsed < 0 {
					return nil, fmt.Errorf(i18n.T("HLS 媒体序号无效"))
				}
				mediaSeq = parsed
				sequenceDeclared = true
			case "#EXT-X-DISCONTINUITY-SEQUENCE":
				if discontinuityDeclared || discontinuitySeen || appended > 0 || pending || value == "" || strings.Trim(value, "0123456789") != "" {
					return nil, i18n.NewError("HLS 不连续序号格式或位置无效")
				}
				parsed, err := strconv.ParseUint(value, 10, 64)
				if err != nil {
					return nil, i18n.NewError("HLS 不连续序号格式或位置无效")
				}
				discontinuity = parsed
				discontinuityDeclared = true
			case "#EXT-X-VERSION", "#EXT-X-TARGETDURATION", "#EXT-X-PLAYLIST-TYPE", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXT-X-PROGRAM-DATE-TIME", "#EXT-X-DATERANGE", "#EXT-X-ALLOW-CACHE", "#EXT-X-START":
			default:
				return nil, fmt.Errorf(i18n.T("暂不支持 HLS 清单标签 %s"), key)
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !pending {
			return nil, fmt.Errorf(i18n.T("暂不支持 HLS 主清单或无时长的分片"))
		}
		ref, err := url.Parse(line)
		if err != nil {
			return nil, fmt.Errorf(i18n.T("HLS 分片地址无效"))
		}
		resolved := base.ResolveReference(ref)
		resolved.Fragment = ""
		if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Hostname() == "" {
			return nil, fmt.Errorf(i18n.T("HLS 分片地址只支持 HTTP 或 HTTPS"))
		}
		if err := checkActiveKeys(); err != nil {
			return nil, err
		}
		if boundary {
			init := *initialization
			init.period = period
			init.hlsDiscontinuity = discontinuity
			segments = append(segments, init)
			boundary = false
		}
		if len(segments) > 0 && segments[len(segments)-1].init {
			segments[len(segments)-1].hlsDiscontinuity = discontinuity
		}
		segment := hlsSegment{url: resolved.String(), mp4: initialization != nil, period: period, hlsDiscontinuity: discontinuity}
		if mediaSeq > math.MaxInt64-appended {
			return nil, fmt.Errorf(i18n.T("HLS 媒体序号超出限制"))
		}
		if sequenceDeclared {
			segment.liveSequence = mediaSeq + appended
			segment.hasLiveSequence = true
		}
		if keyURL != "" {
			segment.keyURL, segment.seq = keyURL, mediaSeq+appended
			segment.iv = hlsSegmentIV(keyIV, segment.seq)
		}
		appended++
		if ranged, ok := resourceModes[segment.url]; ok && ranged != hasRange {
			return nil, fmt.Errorf(i18n.T("暂不支持同一 HLS 资源混合完整分片与字节范围"))
		}
		resourceModes[segment.url] = hasRange
		if hasRange {
			if !explicitStart {
				if len(segments) == 0 || segments[len(segments)-1].init || segments[len(segments)-1].length == 0 || segments[len(segments)-1].url != segment.url {
					return nil, fmt.Errorf(i18n.T("HLS 分片范围缺少同一资源的前置范围"))
				}
				previous := segments[len(segments)-1]
				if previous.start > math.MaxInt64-previous.length {
					return nil, fmt.Errorf(i18n.T("HLS 分片范围超出限制"))
				}
				rangeStart = previous.start + previous.length
			}
			if rangeStart > math.MaxInt64-(rangeLength-1) {
				return nil, fmt.Errorf(i18n.T("HLS 分片范围超出限制"))
			}
			segment.start, segment.length = rangeStart, rangeLength
		}
		segments = append(segments, segment)
		if len(segments) > maxHLSSegments {
			return nil, fmt.Errorf(i18n.T("HLS 分片数量超过限制"))
		}
		pending = false
		hasRange = false
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf(i18n.T("读取 HLS 清单失败：%w"), err)
	}
	if iframeOnly && encrypted {
		return nil, fmt.Errorf(i18n.T("暂不支持加密的 I 帧专用清单"))
	}
	if pending || hasRange || boundary || !ended || len(segments) == 0 || segments[len(segments)-1].init {
		return nil, fmt.Errorf(i18n.T("暂不支持直播或不完整的 HLS 清单"))
	}
	return segments, nil
}

// tsWriter validates every 188-byte packet boundary before writing a chunk.
type tsWriter struct {
	w      io.Writer
	offset int64
}

func (w *tsWriter) Write(p []byte) (int, error) {
	start := int((188 - w.offset%188) % 188)
	for i := start; i < len(p); i += 188 {
		if p[i] != 0x47 {
			return 0, fmt.Errorf(i18n.T("HLS 分片不是受支持的 MPEG-TS 数据"))
		}
	}
	n, err := w.w.Write(p)
	w.offset += int64(n)
	return n, err
}

func (m *Manager) downloadHLSTS(t *Task) error {
	t.metaMu.Lock()
	expected := t.expectedSHA256
	t.metaMu.Unlock()
	expected, err := NormalizeSHA256(expected)
	if err != nil {
		return err
	}
	original, err := url.Parse(t.sourceURL())
	if err != nil {
		return err
	}
	var segments []hlsSegment
	var base *url.URL
	var live bool
	if t.mediaMode == "dash-fmp4" {
		segments, base, err = m.resolveDASH(t, original)
	} else {
		budget := 16
		segments, base, live, err = m.resolveHLSTS(t, original, original, t.sourceURL(), make(map[string]bool), &budget, 0)
	}
	if err != nil {
		return err
	}
	return m.downloadResolvedMedia(t, original, base, segments, live, expected)
}

// downloadResolvedMedia consumes the selected playlist directly so a mixed
// in-band/external audio group cannot silently switch to a different variant.
func (m *Manager) downloadResolvedMedia(t *Task, original, base *url.URL, segments []hlsSegment, live bool, expected string) error {
	if len(segments) == 0 || original == nil || base == nil {
		return i18n.NewError("媒体分片清单为空")
	}
	var err error
	format, suffix := "", ".ts"
	if segments[0].mp4 {
		format, suffix = "fmp4", ".mp4"
	}
	name := t.nameHint
	if name == "" {
		name = pathrec.DeriveFilename(pathrec.Source{URL: t.sourceURL(), Title: t.title})
	}
	name = pathrec.SanitizeFilename(name)
	if ext := filepath.Ext(name); strings.EqualFold(ext, ".m3u8") || strings.EqualFold(ext, ".mpd") || strings.EqualFold(ext, ".ts") || strings.EqualFold(ext, ".mp4") {
		name = strings.TrimSuffix(name, ext)
	}
	name = pathrec.SanitizeFilename(name + suffix)
	if err := os.MkdirAll(t.directory, 0o755); err != nil {
		return err
	}
	t.metaMu.Lock()
	filePath := t.filePath
	t.metaMu.Unlock()
	if filePath != "" && !strings.EqualFold(filepath.Ext(filePath), suffix) {
		return fmt.Errorf(i18n.T("HLS 媒体格式已改变，请新建任务以免使用错误的文件扩展名"))
	}
	part := filePath + ".part"
	if filePath == "" {
		filePath, part, err = claimPath(t.directory, name)
		if err != nil {
			return err
		}
		t.metaMu.Lock()
		t.filePath = filePath
		t.metaMu.Unlock()
	}
	out, err := os.OpenFile(extendedPath(part), os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := m.prepareOutputLimit(t, out, -1); err != nil {
		return err
	}
	playlist := hlsPlaylistFingerprint(segments)
	t.checkpointMu.Lock()
	records := append([]hlsCheckpoint(nil), t.hlsCompleted...)
	savedPlaylist := t.hlsPlaylist
	oldSplice := t.hlsSplice
	t.checkpointMu.Unlock()
	if oldSplice != nil && oldSplice.FingerprintVersion != mp4FingerprintVersion {
		if live && len(records) > 0 {
			return i18n.NewError("旧版直播检查点无法安全恢复，请重新开始录制")
		}
		records, savedPlaylist = nil, ""
		t.checkpointMu.Lock()
		t.hlsSplice = nil
		t.checkpointMu.Unlock()
	}
	t.metaMu.Lock()
	failedChecksum := t.failedSHA256
	t.metaMu.Unlock()
	count, offset := 0, int64(0)
	ranges := make(map[string]hlsRangeState)
	if live {
		// 直播窗口持续滚动，历史分片无法回头校验；从上次进度直接续录。
		if len(records) > 0 {
			count, offset = len(records), records[len(records)-1].End
		}
	} else if playlist == savedPlaylist && (!hasRepeatedInitialization(segments) || oldSplice != nil) && !(failedChecksum != "" && (expected == failedChecksum || expected == "")) {
		count, offset, ranges, err = m.validatedHLSPrefix(t, out, original, base, segments, records, expected)
		if err != nil {
			return fmt.Errorf(i18n.T("无法验证 HLS 续传分片：%w"), err)
		}
	}
	// 缝合状态对应完整校验的前缀；部分校验的前缀含缝合内容时整体重拼，避免
	// 时间轴平移基准与实际内容错位。
	if count < len(records) {
		t.checkpointMu.Lock()
		splice := t.hlsSplice
		if splice != nil && (splice.Period > 0 || splice.ShiftActive || splice.PendingShift) {
			count, offset = 0, 0
			ranges = make(map[string]hlsRangeState)
			t.hlsSplice = nil
		}
		// Even an unshifted splice contains the old prefix's lastEnd.
		t.hlsSplice = nil
		t.checkpointMu.Unlock()
	}
	t.checkpointMu.Lock()
	t.completed, t.resumeTotal, t.resumeValidator, t.resumeResource = nil, 0, "", ""
	t.hlsPlaylist = playlist
	t.hlsFormat = format
	t.hlsCompleted = append([]hlsCheckpoint(nil), records[:count]...)
	if count == 0 {
		t.hlsSplice = nil
	}
	t.metaMu.Lock()
	t.failedSHA256 = ""
	t.metaMu.Unlock()
	err = m.saveTaskLocked(t)
	t.checkpointMu.Unlock()
	if err != nil {
		return fmt.Errorf(i18n.T("无法保存下载记录：%w"), err)
	}
	if err := out.Truncate(offset); err != nil {
		return err
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	t.total.Store(-1)
	t.done.Store(offset)
	t.rangeSupport.Store(0)
	t.resumeSafe.Store(count > 0)
	t.resetSegments(nil)
	progress := t.startSegment(chunkSpan{start: 0, end: -1})
	progress.done.Store(offset)
	safePrefix := true // existing records were verified above
	finished := false
	defer func() {
		if !finished {
			t.removeSegment(progress)
		}
	}()
	mp4 := &hlsMP4State{}
	if segments[0].mp4 && count > 0 {
		if err := mp4.accept(out, 0, records[0].End, true); err != nil {
			return fmt.Errorf(i18n.T("HLS 初始化段无效：%w"), err)
		}
		// 恢复的初始化段同样登记指纹，重复初始化段才能继续复用。
		captureInitialization(mp4, out, 0, records[0].End)
		t.checkpointMu.Lock()
		splice := t.hlsSplice
		t.checkpointMu.Unlock()
		if splice != nil {
			mp4.restoreSplice(splice)
		} else if !live {
			if err := rebuildMP4Prefix(mp4, out, segments, records[:count]); err != nil {
				return fmt.Errorf(i18n.T("HLS 续传时间轴无效：%w"), err)
			}
		}
	}
	if live {
		if err := m.recordHLSLive(t, out, progress, original, base, segments, count, records, ranges, mp4, expected, &safePrefix); err != nil {
			return err
		}
	} else {
		for _, segment := range segments[count:] {
			if err := m.appendHLSSegment(t, out, progress, original, base, segment, ranges, mp4, expected, &safePrefix, false); err != nil {
				return err
			}
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	written := t.done.Load()
	t.finishStream(progress, written)
	finished = true
	t.total.Store(written)
	return m.finishFile(t, part)
}

// appendHLSSegment 下载一个分片并推进检查点；只在完整写入并同步后落盘记录。
func (m *Manager) appendHLSSegment(t *Task, out *os.File, progress *segmentProgress, original, base *url.URL, segment hlsSegment, ranges map[string]hlsRangeState, mp4 *hlsMP4State, expected string, safePrefix *bool, live bool) error {
	start := t.done.Load()
	var record hlsCheckpoint
	if err := m.downloadHLSSegmentWithRetry(t, out, progress, original, base, segment, ranges, &record); err != nil {
		return err
	}
	record.End = t.done.Load()
	if live {
		record.LiveKey = hlsLiveKey(segment)
	}
	if segment.mp4 {
		if segment.init && len(mp4.tracks) > 0 {
			// 重复初始化段：一致时跳过写入，但保留计划位置和远端版本。
			if _, err := reuseInitialization(mp4, out, start, record.End); err != nil {
				return err
			}
			var err error
			record.SHA256, err = chunkDigest(out, chunkSpan{start: start, end: record.End - 1})
			if err != nil {
				return err
			}
			if err := out.Truncate(start); err != nil {
				return err
			}
			if err := out.Sync(); err != nil {
				return fmt.Errorf(i18n.T("无法同步 HLS 分片：%w"), err)
			}
			if _, err := out.Seek(start, io.SeekStart); err != nil {
				return err
			}
			t.done.Store(start)
			progress.done.Store(start)
			record.End, record.SkippedInit = start, true
			t.checkpointMu.Lock()
			t.hlsCompleted = append(t.hlsCompleted, record)
			t.hlsSplice = mp4.spliceState()
			err = m.saveTaskLocked(t)
			t.checkpointMu.Unlock()
			if err != nil {
				return fmt.Errorf(i18n.T("无法保存 HLS 分片进度：%w"), err)
			}
			if record.Validator == "" {
				*safePrefix = false
			}
			t.resumeSafe.Store(*safePrefix)
			return nil
		}
		if !segment.init && (mp4.pendingShift || mp4.shiftActive) {
			if err := mp4.shiftTfdt(out, start, record.End); err != nil {
				return fmt.Errorf(i18n.T("HLS MP4 分片无效：%w"), err)
			}
		}
		if err := mp4.accept(out, start, record.End, segment.init); err != nil {
			return fmt.Errorf(i18n.T("HLS MP4 分片无效：%w"), err)
		}
		if segment.init {
			captureInitialization(mp4, out, start, record.End)
		}
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf(i18n.T("无法同步 HLS 分片：%w"), err)
	}
	var err error
	record.SHA256, err = chunkDigest(out, chunkSpan{start: start, end: record.End - 1})
	if err != nil {
		return fmt.Errorf(i18n.T("无法校验 HLS 分片：%w"), err)
	}
	t.checkpointMu.Lock()
	t.hlsCompleted = append(t.hlsCompleted, record)
	t.hlsSplice = mp4.spliceState()
	err = m.saveTaskLocked(t)
	t.checkpointMu.Unlock()
	if err != nil {
		return fmt.Errorf(i18n.T("无法保存 HLS 分片进度：%w"), err)
	}
	if record.Validator == "" && expected == "" {
		*safePrefix = false
	}
	t.resumeSafe.Store(*safePrefix)
	return nil
}

// recordHLSLive 跟随直播清单录制新出现的分片，直到清单出现结束标签或任务
// 被取消或用户请求结束；普通取消保留检查点。清单滚动中或瞬时不可读
// 会等待重试，失败三次后报错，继续时可从检查点续录。
func (m *Manager) recordHLSLive(t *Task, out *os.File, progress *segmentProgress, original, base *url.URL, window []hlsSegment, count int, records []hlsCheckpoint, ranges map[string]hlsRangeState, mp4 *hlsMP4State, expected string, safePrefix *bool) error {
	wake := t.beginLive()
	m.notify()
	defer func() { t.endLive(); m.notify() }()
	initialization := hlsWindowInitialization(window)
	seen := make(map[string]bool, len(window)+len(records)+64)
	order := make([]string, 0, len(window)+len(records)+64)
	mark := func(u string) {
		if u == "" || seen[u] {
			return
		}
		if len(order) >= 16384 {
			delete(seen, order[0])
			order = order[1:]
		}
		seen[u] = true
		order = append(order, u)
	}
	hasSavedIdentity := false
	for _, record := range records {
		key := sanitizeHLSCheckpoint(record).LiveKey
		if key != "" {
			hasSavedIdentity = true
			mark(key)
		}
	}
	if len(records) > 0 && !hasSavedIdentity {
		// Older records without identities can only use the historical prefix
		// approximation; new records compare each current segment separately.
		for _, segment := range window[:min(count, len(window))] {
			mark(hlsLiveKey(segment))
		}
	}
	{
		for _, segment := range window {
			if seen[hlsLiveKey(segment)] || seen[hlsUnsequencedLiveKey(segment)] || seen[hlsLegacyLiveKey(segment.url)] {
				continue
			}
			if err := m.appendHLSSegment(t, out, progress, original, base, segment, ranges, mp4, expected, safePrefix, true); err != nil {
				if errors.Is(err, contextCanceled) {
					return contextCanceled
				}
				return err
			}
			mark(hlsLiveKey(segment))
		}
	}
	for {
		if t.finishLive.Load() {
			return nil
		}
		select {
		case <-t.cancel:
			return contextCanceled
		case <-wake:
			return nil
		case <-time.After(3 * time.Second):
		}
		if t.finishLive.Load() {
			return nil
		}
		// 本轮清单地址在三次重试间保持不变；base 只在读取成功后更新，
		// 避免失败返回的 nil 被后续调用当作来源页或解析基准。
		address := base.String()
		var data []byte
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			var fresh *url.URL
			data, fresh, err = m.readHLSPlaylistOnceWithWake(t, original, base, address, wake)
			select {
			case <-t.cancel:
				return contextCanceled
			default:
			}
			if t.finishLive.Load() {
				return nil
			}
			if errors.Is(err, contextCanceled) {
				return contextCanceled
			}
			if err == nil {
				base = fresh
				break
			}
			select {
			case <-t.cancel:
				return contextCanceled
			case <-wake:
				return nil
			case <-time.After(3 * time.Second):
			}
			if t.finishLive.Load() {
				return nil
			}
		}
		if err != nil {
			return err
		}
		segments, ended, parseErr := parseHLSWindow(data, base)
		if parseErr != nil {
			if errors.Is(parseErr, errHLSLiveInitSwitch) {
				return parseErr
			}
			if !ended {
				// 清单可能正在写入，下一轮再试。
				continue
			}
			return parseErr
		}
		if hasRepeatedInitialization(segments) || hlsLiveInitializationChanged(initialization, segments) {
			return errHLSLiveInitSwitch
		}
		for _, segment := range segments {
			if seen[hlsLiveKey(segment)] || seen[hlsUnsequencedLiveKey(segment)] || seen[hlsLegacyLiveKey(segment.url)] {
				continue
			}
			if err := m.appendHLSSegment(t, out, progress, original, base, segment, ranges, mp4, expected, safePrefix, true); err != nil {
				if errors.Is(err, contextCanceled) {
					return contextCanceled
				}
				return err
			}
			mark(hlsLiveKey(segment))
		}
		if ended {
			return nil
		}
	}
}

// Retry an interrupted segment in place without downloading earlier segments
// again. Only a fully received segment is added to the persistent checkpoint.
func (m *Manager) downloadHLSSegmentWithRetry(t *Task, out *os.File, progress *segmentProgress, original, page *url.URL, segment hlsSegment, ranges map[string]hlsRangeState, record *hlsCheckpoint) error {
	start := t.done.Load()
	previous, existed := ranges[segment.url]
	for attempt := 0; ; attempt++ {
		*record = hlsCheckpoint{}
		err := m.downloadHLSSegment(t, out, progress, original, page, segment, ranges, record)
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
		if err := out.Truncate(start); err != nil {
			return err
		}
		if _, err := out.Seek(start, io.SeekStart); err != nil {
			return err
		}
		t.done.Store(start)
		progress.done.Store(start)
		if existed {
			ranges[segment.url] = previous
		} else {
			delete(ranges, segment.url)
		}
	}
}

// hlsRequest never forwards the original task's credentials to another origin.
func hlsRequest(t *Task, original, page *url.URL, address string) (*http.Request, func(), error) {
	req, cancel, err := t.newRequest(address)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Del("Range")
	req.Header.Del("If-Range")
	if !sameOrigin(original, req.URL) {
		// net/http may synthesize Basic authorization from URL userinfo even
		// after the Authorization header has been removed.
		if req.URL.User != nil {
			cancel()
			return nil, nil, i18n.NewError("跨站媒体地址不能包含登录凭据")
		}
		for key := range req.Header {
			switch http.CanonicalHeaderKey(key) {
			case "Accept", "Accept-Encoding", "Accept-Language", "User-Agent":
			default:
				delete(req.Header, key)
			}
		}
		if referer := sourceReferer(page.String(), req.URL); referer != "" {
			req.Header.Set("Referer", referer)
		}
	}
	return req, cancel, nil
}

// hlsPlaylistEnded 报告媒体清单是否带结束标签（点播）。
func hlsPlaylistEnded(data []byte) bool {
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 4096), maxHLSPlaylist)
	for scan.Scan() {
		if strings.TrimSpace(scan.Text()) == "#EXT-X-ENDLIST" {
			return true
		}
	}
	return false
}

// parseHLSWindow 解析媒体清单的当前窗口。直播清单没有结束标签；补齐标签后
// 按同一解析规则读取，返回的 ended 报告是否为点播。
func parseHLSWindow(data []byte, base *url.URL) ([]hlsSegment, bool, error) {
	ended := hlsPlaylistEnded(data)
	if !ended {
		data = append(append([]byte(nil), data...), "\n#EXT-X-ENDLIST\n"...)
	}
	segments, err := parseHLSTS(data, base)
	if err == nil && !ended && hasRepeatedInitialization(segments) {
		return nil, false, errHLSLiveInitSwitch
	}
	return segments, ended, err
}

func hlsWindowInitialization(segments []hlsSegment) *hlsSegment {
	for _, segment := range segments {
		if segment.init {
			copy := segment
			return &copy
		}
	}
	return nil
}

func hlsLiveInitializationChanged(previous *hlsSegment, segments []hlsSegment) bool {
	current := hlsWindowInitialization(segments)
	return (previous == nil) != (current == nil) || (previous != nil && !equalHLSSegment(*previous, *current))
}

// readHLSPlaylist 下载并读取一个清单；只接受 HTTP(S) 与恒等内容编码。
func (m *Manager) readHLSPlaylist(t *Task, original, page *url.URL, address string) ([]byte, *url.URL, error) {
	return m.readHLSPlaylistWithWake(t, original, page, address, nil)
}

func (m *Manager) readHLSPlaylistWithWake(t *Task, original, page *url.URL, address string, wake <-chan struct{}) ([]byte, *url.URL, error) {
	for attempt := 0; ; attempt++ {
		data, base, err := m.readHLSPlaylistOnceWithWake(t, original, page, address, wake)
		if err == nil {
			return data, base, nil
		}
		var readErr *responseReadError
		if !errors.As(err, &readErr) || !transientError(readErr) || attempt == maxRequestAttempts-1 {
			return nil, nil, err
		}
		timer := time.NewTimer(time.Duration(250*(1<<attempt)) * time.Millisecond)
		select {
		case <-timer.C:
		case <-t.cancel:
			timer.Stop()
			return nil, nil, contextCanceled
		case <-wake:
			timer.Stop()
			return nil, nil, contextCanceled
		}
	}
}

func (m *Manager) readHLSPlaylistOnce(t *Task, original, page *url.URL, address string) ([]byte, *url.URL, error) {
	return m.readHLSPlaylistOnceWithWake(t, original, page, address, nil)
}

func (m *Manager) readHLSPlaylistOnceWithWake(t *Task, original, page *url.URL, address string, wake <-chan struct{}) ([]byte, *url.URL, error) {
	req, cancel, err := hlsRequest(t, original, page, address)
	if err != nil {
		return nil, nil, err
	}
	defer cancel()
	if wake != nil {
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			select {
			case <-wake:
				cancel()
			case <-finished:
			case <-req.Context().Done():
			}
		}()
	}
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL == nil {
		return nil, nil, fmt.Errorf(i18n.T("无法读取 HLS 清单（HTTP %d）"), resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, nil, fmt.Errorf(i18n.T("HLS 清单使用了不支持的内容编码"))
	}
	base := resp.Request.URL
	if resp.ContentLength > maxHLSPlaylist {
		return nil, nil, fmt.Errorf(i18n.T("HLS 清单超过大小限制"))
	}
	body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
	var data []byte
	if resp.ContentLength >= 0 {
		data, err = m.readExactMediaBodyWithCancel(t, body, resp.ContentLength, i18n.NewError("HLS 清单超过大小限制"), req.Context().Done())
	} else {
		data, err = io.ReadAll(io.LimitReader(mediaNetworkReader{manager: m, task: t, source: body, canceled: req.Context().Done()}, maxHLSPlaylist+1))
	}
	if err != nil {
		return nil, nil, &responseReadError{err: err}
	}
	if len(data) > maxHLSPlaylist {
		return nil, nil, fmt.Errorf(i18n.T("HLS 清单超过大小限制"))
	}
	return data, base, nil
}

// resolveHLSTS bounds recursive master playlists and tries the highest bitrate
// muxed variants first. A lower compatible variant can follow an unsupported one.
// The live result reports a media playlist without an end tag.
func (m *Manager) resolveHLSTS(t *Task, original, page *url.URL, address string, seen map[string]bool, budget *int, depth int) ([]hlsSegment, *url.URL, bool, error) {
	if depth > 4 || *budget <= 0 || seen[address] {
		return nil, nil, false, fmt.Errorf(i18n.T("HLS 主清单嵌套过深或存在循环"))
	}
	*budget--
	seen[address] = true
	data, base, err := m.readHLSPlaylist(t, original, page, address)
	if err != nil {
		return nil, nil, false, err
	}
	variants, master, err := parseHLSMaster(data, base)
	if err != nil {
		return nil, nil, false, err
	}
	if !master {
		segments, ended, err := parseHLSWindow(data, base)
		return segments, base, !ended, err
	}
	variants, err = pickHLSVariants(variants, t.mediaQuality)
	if err != nil {
		return nil, nil, false, err
	}
	var lastErr error
	for _, variant := range variants {
		select {
		case <-t.cancel:
			return nil, nil, false, contextCanceled
		default:
		}
		segments, mediaBase, live, err := m.resolveHLSTS(t, original, base, variant.url, seen, budget, depth+1)
		if err == nil {
			return segments, mediaBase, live, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf(i18n.T("没有可用画质"))
	}
	return nil, nil, false, fmt.Errorf(i18n.T("没有可下载的 HLS 画质：%w"), lastErr)
}

func (m *Manager) downloadHLSSegment(t *Task, out *os.File, progress *segmentProgress, original, page *url.URL, segment hlsSegment, ranges map[string]hlsRangeState, record *hlsCheckpoint) error {
	req, cancel, err := hlsRequest(t, original, page, segment.url)
	if err != nil {
		return err
	}
	defer cancel()
	previous, seen := ranges[segment.url]
	if segment.indexResource != "" {
		if seen && ((segment.indexValidator != "" && previous.validator != segment.indexValidator) || previous.resource != segment.indexResource || previous.total != segment.indexTotal) {
			return i18n.NewError("DASH 索引对应的资源版本发生变化")
		}
		previous = hlsRangeState{validator: segment.indexValidator, resource: segment.indexResource, total: segment.indexTotal}
		seen = true
	}
	if segment.length > 0 {
		if seen && previous.validator == "" {
			t.metaMu.Lock()
			hasChecksum := t.expectedSHA256 != ""
			t.metaMu.Unlock()
			if !hasChecksum {
				return fmt.Errorf(i18n.T("HLS 字节范围缺少可靠的资源版本标识"))
			}
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", segment.start, segment.start+segment.length-1))
		if seen && previous.validator != "" {
			req.Header.Set("If-Range", previous.validator)
		}
	}
	resp, err := m.doWithRetry(t, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	wantStatus := http.StatusOK
	if segment.length > 0 {
		wantStatus = http.StatusPartialContent
	}
	if resp.StatusCode != wantStatus {
		return fmt.Errorf(i18n.T("读取 HLS 分片失败（HTTP %d）"), resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf(i18n.T("HLS 分片使用了不支持的内容编码"))
	}
	if resp.Request == nil || resp.Request.URL == nil {
		return fmt.Errorf(i18n.T("无法确认 HLS 分片的最终地址"))
	}
	initLimit := int64(maxHLSInitialization)
	var plaintext []byte
	if segment.keyURL != "" {
		initLimit += aes.BlockSize
	}
	if segment.init && (segment.length > initLimit || resp.ContentLength > initLimit) {
		return fmt.Errorf(i18n.T("HLS 初始化段超过 16 MiB"))
	}
	record.Resource = resourceFingerprint(resp.Request.URL)
	record.Validator = strongValidator(resp.Header)
	record.ResourceTotal = resp.ContentLength
	if segment.length > 0 {
		prefix := fmt.Sprintf("bytes %d-%d/", segment.start, segment.start+segment.length-1)
		header := strings.TrimSpace(resp.Header.Get("Content-Range"))
		if !strings.HasPrefix(header, prefix) {
			return fmt.Errorf(i18n.T("HLS 分片范围响应无效"))
		}
		totalText := strings.TrimPrefix(header, prefix)
		total, err := strconv.ParseInt(totalText, 10, 64)
		if err != nil || strconv.FormatInt(total, 10) != totalText || total <= segment.start+segment.length-1 || (resp.ContentLength >= 0 && resp.ContentLength != segment.length) {
			return fmt.Errorf(i18n.T("HLS 分片范围或长度无效"))
		}
		validator := strongValidator(resp.Header)
		if resp.Request == nil || resp.Request.URL == nil {
			return fmt.Errorf(i18n.T("无法确认 HLS 分片的最终地址"))
		}
		resource := resourceFingerprint(resp.Request.URL)
		if segment.indexResource != "" && ((segment.indexValidator != "" && validator != segment.indexValidator) || resource != segment.indexResource || total != segment.indexTotal) {
			return i18n.NewError("DASH 索引对应的资源版本发生变化")
		}
		t.metaMu.Lock()
		hasChecksum := t.expectedSHA256 != ""
		t.metaMu.Unlock()
		if seen && (previous.total != total || (!hasChecksum && (previous.resource != resource || previous.validator == "" || validator != previous.validator)) || (hasChecksum && validator != "" && previous.validator != "" && validator != previous.validator)) {
			return fmt.Errorf(i18n.T("HLS 分片资源在下载期间发生变化"))
		}
		if !seen {
			ranges[segment.url] = hlsRangeState{validator: validator, total: total, resource: resource}
		}
		record.ResourceTotal = total
	}
	var target io.Writer = out
	if !segment.mp4 {
		target = &tsWriter{w: out}
	}
	body := idleReader{body: resp.Body, timeout: m.bodyIdleTimeout, cancel: cancel}
	var network io.Reader = body
	if segment.keyURL != "" {
		network = mediaNetworkReader{manager: m, task: t, source: body}
	}
	var source io.Reader = network
	var limited *io.LimitedReader
	if segment.length > 0 {
		limited = &io.LimitedReader{R: network, N: segment.length}
		source = limited
	} else if segment.keyURL != "" && resp.ContentLength >= 0 {
		// Avoid waiting for quota merely to observe EOF once all declared
		// ciphertext has already been read.
		source = io.LimitReader(network, resp.ContentLength)
	}
	if segment.init {
		source = io.LimitReader(source, initLimit+1)
	}
	if segment.keyURL != "" {
		key, err := m.hlsKey(t, original, page, segment.keyURL)
		if err != nil {
			return err
		}
		cipherLength := resp.ContentLength
		if segment.length > 0 {
			cipherLength = segment.length
		}
		plaintext, err = readHLSAESWithLength(source, key, segment.iv, cipherLength)
		if err != nil {
			return err
		}
		if limited != nil && limited.N != 0 {
			return &responseReadError{err: io.ErrUnexpectedEOF}
		}
	}
	writer := &segmentWriter{writer: target, segment: progress}
	var written int64
	if segment.keyURL != "" {
		written, err = writeHLSPlaintext(t, writer, plaintext)
	} else {
		written, err = m.copyWithProgress(t, writer, source)
	}
	if err != nil {
		return err
	}
	if segment.init && written > maxHLSInitialization {
		return fmt.Errorf(i18n.T("HLS 初始化段超过 16 MiB"))
	}
	if written == 0 || (!segment.mp4 && written%188 != 0) || (resp.ContentLength >= 0 && segment.keyURL == "" && written != resp.ContentLength) || (segment.length > 0 && segment.keyURL == "" && written != segment.length) {
		return fmt.Errorf(i18n.T("HLS 分片长度无效"))
	}
	if record.ResourceTotal < 0 {
		record.ResourceTotal = written
	}
	if segment.length > 0 {
		var extra [1]byte
		n, err := body.Read(extra[:])
		if n != 0 {
			return fmt.Errorf(i18n.T("HLS 分片范围响应超长"))
		}
		if err != io.EOF {
			if err == nil {
				err = io.ErrNoProgress
			}
			return &responseReadError{err: err}
		}
	}
	return nil
}
