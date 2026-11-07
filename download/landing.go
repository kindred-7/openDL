package download

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openDL/i18n"

	"golang.org/x/net/html"
)

const maxLandingBytes = 64 << 10

type prefixedDownloadBody struct {
	io.Reader
	io.Closer
}

func isHTMLResponse(response *http.Response) bool {
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return contentType == "text/html" || contentType == "application/xhtml+xml"
}

func isHTMLFilename(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm") || strings.HasSuffix(lower, ".xhtml")
}

// resolveDownloadResponse follows declarative landing-page redirects using the
// same request route and cross-origin header policy as ordinary HTTP redirects.
func resolveDownloadResponse(request *http.Request, rejectHTML bool, send func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	seen := map[string]bool{request.URL.String(): true}
	for hop := 0; hop < 10; hop++ {
		response, err := send(request)
		if err != nil {
			return nil, err
		}
		disposition, _, _ := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
		if response.StatusCode != http.StatusOK || !isHTMLResponse(response) || strings.EqualFold(disposition, "attachment") {
			return response, nil
		}
		actual := response.Request
		if actual == nil || actual.URL == nil {
			actual = request
		}
		target := refreshDestination(response.Header.Get("Refresh"), actual.URL)
		if target == nil && request.Method != http.MethodHead {
			target, err = landingMetaRefresh(response, actual.URL)
			if err != nil {
				response.Body.Close()
				return nil, err
			}
		}
		if target == nil {
			if rejectHTML && request.Method != http.MethodHead {
				response.Body.Close()
				return nil, i18n.NewError("下载地址返回了网页，请在浏览器中完成跳转或登录后重新下载")
			}
			return response, nil
		}
		response.Body.Close()
		if seen[target.String()] {
			return nil, i18n.NewError("下载中转页存在循环跳转")
		}
		seen[target.String()] = true
		next := actual.Clone(request.Context())
		next.URL, next.Host = target, ""
		next.Header.Del("Range")
		next.Header.Del("If-Range")
		if err := checkDownloadRedirect(next, []*http.Request{actual}); err != nil {
			return nil, err
		}
		request = next
	}
	return nil, i18n.NewError("下载中转页跳转次数过多")
}

func refreshDestination(value string, base *url.URL) *url.URL {
	delay, target, found := strings.Cut(value, ";")
	if !found {
		delay, target, found = strings.Cut(value, ",")
	}
	if !found {
		return nil
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(delay), 10, 32); err != nil {
		return nil
	}
	target = strings.TrimSpace(target)
	if len(target) >= 3 && strings.EqualFold(target[:3], "url") {
		if suffix := strings.TrimSpace(target[3:]); strings.HasPrefix(suffix, "=") {
			target = strings.TrimSpace(suffix[1:])
		}
	}
	if len(target) > 0 && (target[0] == '\'' || target[0] == '"') {
		quote := target[0]
		target = target[1:]
		if end := strings.IndexByte(target, quote); end >= 0 {
			target = target[:end]
		}
	}
	if target == "" || len(target) > 4096 {
		return nil
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil
	}
	resolved := base.ResolveReference(parsed)
	resolved.Fragment = ""
	if resolved.Hostname() == "" || (resolved.Scheme != "http" && resolved.Scheme != "https") {
		return nil
	}
	return resolved
}

func landingMetaRefresh(response *http.Response, base *url.URL) (*url.URL, error) {
	body := response.Body
	reader := &idleReader{body: body, timeout: 10 * time.Second, cancel: func() { body.Close() }}
	var prefix bytes.Buffer
	tokens := html.NewTokenizer(io.TeeReader(io.LimitReader(reader, maxLandingBytes), &prefix))
	defer func() {
		response.Body = prefixedDownloadBody{Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), body), Closer: body}
	}()
	baseSeen := false
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			if err := tokens.Err(); err != io.EOF {
				return nil, err
			}
			return nil, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			tag := tokens.Token()
			if tag.Data == "base" && !baseSeen {
				for _, attr := range tag.Attr {
					if attr.Key == "href" {
						if target, err := base.Parse(attr.Val); err == nil {
							base, baseSeen = target, true
						}
					}
				}
			}
			if tag.Data != "meta" {
				continue
			}
			var equivalent, content string
			for _, attr := range tag.Attr {
				switch attr.Key {
				case "http-equiv":
					equivalent = strings.TrimSpace(attr.Val)
				case "content":
					content = attr.Val
				}
			}
			if strings.EqualFold(equivalent, "refresh") {
				if target := refreshDestination(content, base); target != nil {
					return target, nil
				}
			}
		}
	}
}
