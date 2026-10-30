// Package i18n localizes presentation text. Source messages are Simplified
// Chinese; identifiers, persisted state and user content must stay untranslated.
package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

const (
	Chinese = "zh-CN"
	English = "en"
)

//go:embed en.json
var englishJSON []byte

var english = func() map[string]string {
	var messages map[string]string
	if err := json.Unmarshal(englishJSON, &messages); err != nil {
		panic("i18n: invalid embedded English catalog: " + err.Error())
	}
	return messages
}()

var current atomic.Bool

// Normalize accepts older language tags and falls back to the original language.
func Normalize(locale string) string {
	locale = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	if locale == "en" || strings.HasPrefix(locale, "en-") {
		return English
	}
	return Chinese
}

// Set is safe for the UI thread and background download/notification workers.
func Set(locale string) { current.Store(Normalize(locale) == English) }

func Locale() string {
	if current.Load() {
		return English
	}
	return Chinese
}

// T returns the source text for unknown messages, preserving a usable fallback.
// Translate a format string before formatting it, never interpolated user text.
func T(source string) string {
	if current.Load() {
		if translated, ok := english[source]; ok {
			return translated
		}
	}
	return source
}

func F(source string, args ...any) string { return fmt.Sprintf(T(source), args...) }

type messageError struct{ source string }

func (e *messageError) Error() string { return T(e.source) }

// NewError keeps sentinel identity while translating at the time of display.
func NewError(source string) error { return &messageError{source: source} }
