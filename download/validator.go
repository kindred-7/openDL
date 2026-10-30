package download

import (
	"net/http"
	"strings"
	"time"
)

// strongValidator selects a validator suitable for If-Range. For a date we
// require a conservative 60-second separation from the server's Date. A weak
// or malformed ETag must not be replaced by a date in an If-Range request.
func strongValidator(h http.Header) string {
	etag := strings.TrimSpace(h.Get("ETag"))
	if etag != "" {
		if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' {
			return ""
		}
		for i := 1; i < len(etag)-1; i++ {
			b := etag[i]
			if b != 0x21 && !(b >= 0x23 && b <= 0x7e) && b < 0x80 {
				return ""
			}
		}
		return etag
	}
	modified := strings.TrimSpace(h.Get("Last-Modified"))
	last, err := http.ParseTime(modified)
	if err != nil {
		return ""
	}
	date, err := http.ParseTime(h.Get("Date"))
	if err != nil || date.Sub(last) < 60*time.Second {
		return ""
	}
	return modified
}
