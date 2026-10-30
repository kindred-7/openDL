package style

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

// 设计规则的字体（docs/design-rules.md）：Geist 排拉丁，MiSans 排中文。
// 只内嵌文档用到的字重：MiSans 400/500；Geist 是可变字重。
//
//go:embed fonts/Geist.ttf
var geist []byte

//go:embed fonts/MiSans-Regular.ttf
var miSansRegular []byte

//go:embed fonts/MiSans-Medium.ttf
var miSansMedium []byte

// RegisterFonts 注册设计规则字体，应用启动时调用一次；之后文本用
// FontFamily（"Geist, MiSans"）即可拿到拉丁 + 中文的正确排版。
func RegisterFonts() error {
	for _, f := range []struct {
		data   []byte
		family string
	}{
		{geist, "Geist"},
		{miSansRegular, "MiSans"},
		{miSansMedium, "MiSans"},
	} {
		if err := ui.RegisterFont(f.data, f.family); err != nil {
			return err
		}
	}
	return nil
}
