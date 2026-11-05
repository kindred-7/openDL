//go:build !windows

package extensionstore

import "errors"

func DefaultURL() string { return chromeURL }

func Open(Browser) error { return errors.ErrUnsupported }
