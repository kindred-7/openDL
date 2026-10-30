package download

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"openDL/i18n"
)

type hlsVariant struct {
	url       string
	bandwidth int64
	height    int
	audio     string
}

// hlsResolutionHeight reads the height of RESOLUTION=WxH. The attribute is
// only advisory for quality capping, so a missing or malformed value becomes
// zero ("unknown") instead of failing the whole playlist.
func hlsResolutionHeight(value string) int {
	if value == "" {
		return 0
	}
	i := strings.IndexAny(value, "xX")
	if i <= 0 || i == len(value)-1 {
		return 0
	}
	width, errW := strconv.Atoi(value[:i])
	height, errH := strconv.Atoi(value[i+1:])
	if errW != nil || errH != nil || width <= 0 || height <= 0 || width > 21600 || height > 21600 {
		return 0
	}
	return height
}

// NormalizeMediaQuality validates the optional height cap ("720") a caller
// picked for a media download. The empty string means automatic selection.
func NormalizeMediaQuality(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	height, err := strconv.Atoi(value)
	if err != nil || height < 16 || height > 21600 {
		return "", fmt.Errorf(i18n.T("画质参数无效"))
	}
	return strconv.Itoa(height), nil
}

// pickHLSVariants applies a height cap to the parsed variants while keeping
// bandwidth order. Variants that never declared a resolution are kept behind
// capped ones, so masters without RESOLUTION still download instead of
// failing on an unenforceable cap.
func pickHLSVariants(variants []hlsVariant, quality string) ([]hlsVariant, error) {
	if quality == "" {
		return variants, nil
	}
	maxHeight, err := strconv.Atoi(quality)
	if err != nil || maxHeight < 16 || maxHeight > 21600 {
		return nil, fmt.Errorf(i18n.T("画质参数无效"))
	}
	var known, unknown []hlsVariant
	for _, variant := range variants {
		switch {
		case variant.height <= 0:
			unknown = append(unknown, variant)
		case variant.height <= maxHeight:
			known = append(known, variant)
		}
	}
	if len(known) == 0 && len(unknown) == 0 {
		return nil, fmt.Errorf(i18n.T("清单中没有不高于 %dp 的画质"), maxHeight)
	}
	return append(known, unknown...), nil
}

// hlsAttributes parses the quoted values used in HLS attribute lists. Commas
// inside a quoted CODECS or NAME value do not delimit another attribute.
func hlsAttributes(line string) (map[string]string, error) {
	values := make(map[string]string)
	for len(line) > 0 {
		name, rest, found := strings.Cut(line, "=")
		if !found || name == "" || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf(i18n.T("HLS 属性格式无效"))
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return nil, fmt.Errorf(i18n.T("HLS 属性引号未闭合"))
			}
			value, rest = rest[1:1+end], rest[2+end:]
		} else if i := strings.IndexByte(rest, ','); i >= 0 {
			value, rest = rest[:i], rest[i:]
		} else {
			value, rest = rest, ""
		}
		if value == "" || values[name] != "" {
			return nil, fmt.Errorf(i18n.T("HLS 属性缺失或重复"))
		}
		values[name] = value
		if rest == "" {
			break
		}
		if rest[0] != ',' || len(rest) == 1 {
			return nil, fmt.Errorf(i18n.T("HLS 属性分隔符无效"))
		}
		line = rest[1:]
	}
	return values, nil
}

// parseHLSMaster returns muxed variants in descending bandwidth order. Audio
// groups with separate URIs are skipped because this downloader cannot mux them.
func parseHLSMaster(data []byte, base *url.URL) ([]hlsVariant, bool, error) {
	return parseHLSMasterVariants(data, base, false)
}

func parseHLSMasterVariants(data []byte, base *url.URL, includeExternal bool) ([]hlsVariant, bool, error) {
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 4096), maxHLSPlaylist)
	if !scan.Scan() || strings.TrimSpace(scan.Text()) != "#EXTM3U" {
		return nil, false, fmt.Errorf(i18n.T("不是有效的 HLS 播放清单"))
	}
	var variants []hlsVariant
	groups := map[string]bool{}
	var pending map[string]string
	master := false
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			master = true
			if pending != nil {
				return nil, true, fmt.Errorf(i18n.T("HLS 主清单缺少画质地址"))
			}
			var err error
			pending, err = hlsAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			if err != nil {
				return nil, true, err
			}
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			attrs, err := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			if err != nil {
				return nil, true, err
			}
			if attrs["TYPE"] == "AUDIO" && attrs["GROUP-ID"] != "" {
				id := attrs["GROUP-ID"]
				groups[id] = groups[id] || attrs["URI"] != ""
			}
		case strings.HasPrefix(line, "#EXT-X-SESSION-KEY:"):
			// Session keys are preload hints, not an encryption state inherited
			// by a media playlist. Segment decryption is determined only by the
			// EXT-X-KEY tags in that playlist; unsupported methods still fail there.
			attrs, err := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-SESSION-KEY:"))
			if err != nil {
				return nil, true, err
			}
			if attrs["METHOD"] == "" || attrs["METHOD"] == "NONE" || attrs["URI"] == "" {
				return nil, true, fmt.Errorf(i18n.T("HLS 属性缺失或重复"))
			}
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		default:
			if pending == nil {
				if master {
					return nil, true, fmt.Errorf(i18n.T("HLS 主清单格式无效"))
				}
				continue
			}
			bandwidth, err := strconv.ParseInt(pending["BANDWIDTH"], 10, 64)
			if err != nil || bandwidth <= 0 {
				return nil, true, fmt.Errorf(i18n.T("HLS 画质码率无效"))
			}
			ref, err := url.Parse(line)
			if err != nil {
				return nil, true, fmt.Errorf(i18n.T("HLS 画质地址无效"))
			}
			resolved := base.ResolveReference(ref)
			resolved.Fragment = ""
			if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Hostname() == "" {
				return nil, true, fmt.Errorf(i18n.T("HLS 画质地址只支持 HTTP 或 HTTPS"))
			}
			variants = append(variants, hlsVariant{
				url: resolved.String(), bandwidth: bandwidth,
				height: hlsResolutionHeight(pending["RESOLUTION"]), audio: pending["AUDIO"],
			})
			if len(variants) > 1000 {
				return nil, true, fmt.Errorf(i18n.T("HLS 画质数量超过限制"))
			}
			pending = nil
		}
	}
	if err := scan.Err(); err != nil {
		return nil, master, fmt.Errorf(i18n.T("读取 HLS 清单失败：%w"), err)
	}
	if !master {
		return nil, false, nil
	}
	if pending != nil {
		return nil, true, fmt.Errorf(i18n.T("HLS 主清单缺少画质地址"))
	}
	filtered := variants[:0]
	for _, variant := range variants {
		if variant.audio != "" {
			external, ok := groups[variant.audio]
			if !ok || (external && !includeExternal) {
				continue
			}
		}
		filtered = append(filtered, variant)
	}
	if len(filtered) == 0 {
		return nil, true, fmt.Errorf(i18n.T("HLS 主清单仅包含独立音轨或无可用画质"))
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].bandwidth > filtered[j].bandwidth })
	return filtered, true, nil
}
