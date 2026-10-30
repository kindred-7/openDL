package download

import (
	"strings"

	"golang.org/x/text/language"
	"openDL/i18n"
)

func NormalizeAudioLanguage(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 64 {
		return "", i18n.NewError("音轨语言格式无效")
	}
	tag, err := language.Parse(value)
	if err != nil || tag == language.Und {
		return "", i18n.NewError("音轨语言格式无效")
	}
	return tag.String(), nil
}

// Exact tags take precedence; a shared primary language is an explicit
// fallback for manifests that declare only a language or a different region.
func audioLanguageRank(wanted, actual string) int {
	if wanted == "" {
		return 1
	}
	if strings.EqualFold(wanted, actual) {
		return 2
	}
	wantTag, err := language.Parse(wanted)
	if err != nil {
		return 0
	}
	actualTag, err := language.Parse(actual)
	if err != nil {
		return 0
	}
	actualRaw, _, _ := actualTag.Raw()
	if actualRaw.String() == "und" {
		return 0
	}
	wantBase, _ := wantTag.Base()
	actualBase, _ := actualTag.Base()
	if wantBase == actualBase && wantBase.String() != "und" {
		return 1
	}
	return 0
}
