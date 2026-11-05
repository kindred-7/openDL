package extensionstore

import "errors"

type Browser string

const (
	Chrome  Browser = "Chrome"
	Edge    Browser = "Microsoft Edge"
	Firefox Browser = "Firefox"

	chromeURL  = "https://chromewebstore.google.com/detail/opendl/gppemfjpooijagmiigefeonekdkiodah"
	firefoxURL = "https://addons.mozilla.org/firefox/addon/opendl/"
)

var ErrBrowserNotFound = errors.New("browser executable not found")

func (b Browser) storeURL() string {
	if b == Firefox {
		return firefoxURL
	}
	// Edge can install the Chrome Web Store listing while its own listing is under review.
	return chromeURL
}
