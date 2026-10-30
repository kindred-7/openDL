package style

import "github.com/egoist/mygo/ui"

// 设计规则：docs/design.html 的样式、字号字重沉淀（详见 docs/design-rules.md）。
//
//	字体   Geist（拉丁）+ MiSans（中文），正文使用 400 / 500，品牌使用较粗字重
//	颜色   Radix Gray 中性色与黑白主色，Green/Amber/Red 状态色
//	其余   间距、圆角、高度、组件规格照设计稿固定

// ── 字体与文字 ───────────────────────────────────────────────────────────

// FontFamily 是文本默认字体链：Geist 排拉丁，缺字（中文）由 MiSans 兜底。
const FontFamily = "Geist, MiSans"

// 字号（正文 13px/1.4；11 说明、12 次要、18 数值、36 概览进度）。
const (
	TextXS     float32 = 11 // 说明、标签、徽章、kbd、图表标注
	TextSM     float32 = 12 // 次要信息、KV 值、状态栏、字段
	TextBase   float32 = 13 // 正文与控件文字
	TextSearch float32 = 12 // 标题栏紧凑搜索框 .cmd
	TextTitle  float32 = 15 // 对话框标题、详情文件名
	TextBrand  float32 = 16 // 标题栏品牌
	TextLG     float32 = 18 // 统计卡数值
	TextHero   float32 = 36 // 详情概览进度数值
)

// 字重：设计稿只用 Regular 与 Medium。
const (
	WeightRegular     = 400
	WeightMedium      = 500
	WeightBrand       = 600
	WeightBrandStrong = 700
)

// LineHeight 是正文行高倍数（13px/1.4）。
const LineHeight = 1.4

// ── 度量（照设计稿 CSS 固定） ───────────────────────────────────────────

// 圆角。
const (
	RadiusXS float32 = 5  // 徽章 .bd
	RadiusSM float32 = 6  // 按钮 .bt/.ib、导航 .nav、输入 .cmd
	RadiusMD float32 = 8  // 卡片 .card/.lim、菜单 .menu
	RadiusLG float32 = 10 // 应用外壳 .app
)

// 高度与固定尺寸。
const (
	WidthSidebar    float32 = 220
	NavItemGap      float32 = 2  // 菜单背景之间的固定间距，不随悬停改变
	HeightTitleBar  float32 = 38 // 标题行 .ttl
	HeightToolbar   float32 = 46 // 工具栏 .tb
	HeightButton    float32 = 28 // 按钮 .bt
	SizeIconButton  float32 = 28 // 图标按钮 .ib
	HeightTableHead float32 = 30 // 表头 .g.th
	HeightRow       float32 = 46 // 任务行 .r
	HeightStatusBar float32 = 28 // 状态栏 .st
	HeightProgress  float32 = 4  // 进度条 .pb
	SizeCheckbox    float32 = 15 // 复选框 .cb
	SizeSwitchW     float32 = 28 // 开关 .sw
	SizeSwitchH     float32 = 16
	SizeIcon        float32 = 16   // 图标 .i
	StrokeIcon      float32 = 1.75 // 图标描边
	BorderWidth     float32 = 1    // 描边一律 1px
)

// 间距刻度。
const (
	GapXS float32 = 4
	GapSM float32 = 8
	GapMD float32 = 10
	GapLG float32 = 12
	GapXL float32 = 18
)

// Badge 是状态徽章 .bd 的规格。
const (
	BadgeFont   float32 = TextXS
	BadgePadY   float32 = 1
	BadgePadX   float32 = 7
	BadgeRadius float32 = RadiusXS
)

// Kbd 是快捷键芯片 kbd 的规格。
const (
	KbdFont   float32 = TextXS
	KbdPadX   float32 = 5
	KbdRadius float32 = 4
)

// TaskTable 是任务表的间距令牌。列宽、优先级及显隐偏好由
// page_columns.go 的统一列模型计算，表头和行共享同一组宽度。
var TaskTable = struct {
	Gap, PadX float32
}{GapSM, GapLG + 2}

// ── 颜色（design.html） ──────────────────────────────────────────────────

// Colors 保存设计稿的语义色，所有原生组件共用同一套亮/深主题。
type Colors struct {
	Page         ui.Color // 页面底色
	Bg           ui.Color // 窗口/卡片面
	Panel        ui.Color // 面板：侧栏、工具栏、统计卡底
	Fg           ui.Color // 主文字
	Muted        ui.Color // 次要文字
	Faint        ui.Color // 弱化文字、计数
	Border       ui.Color // 描边
	BorderStrong ui.Color // 强描边（输入框、徽章、芯片）

	Primary                      ui.Color // 主按钮、进度填充、选中块
	Brand                        ui.Color // 品牌主色，默认随主题主色变化
	OnPrimary                    ui.Color // 主色上的文字
	Selection                    ui.Color // 选中底
	Hover, Pressed               ui.Color
	PrimaryHover, PrimaryPressed ui.Color
	Focus                        ui.Color // 焦点环

	Success ui.Color // 已完成
	Warning ui.Color // 排队/夜间
	Danger  ui.Color // 失败/删除
}

// NewRules 保留设计稿度量，按用户规则将色彩映射为 Radix 亮/深语义色。
func NewRules(dark bool) *Rules {
	if dark {
		return &Rules{Font: FontFamily, Colors: darkColors(), Dark: true}
	}
	return &Rules{Font: FontFamily, Colors: lightColors()}
}

// Rules 是一套完整的设计规则：字体 + 语义色。
type Rules struct {
	Font   string
	Colors Colors
	Dark   bool
}

// Theme 把规则映射到 MyGo 的 Theme，供内置控件取色。
func (r *Rules) Theme() *ui.Theme {
	c := r.Colors
	return &ui.Theme{
		Dark: r.Dark, Background: c.Bg,
		Surface: c.Bg, SurfaceHover: c.Hover, SurfacePressed: c.Pressed,
		Border: c.Border, Text: c.Fg, TextMuted: c.Muted,
		Accent: c.Primary, AccentHover: c.PrimaryHover, AccentPressed: c.PrimaryPressed, AccentText: c.OnPrimary,
		Danger: c.Danger, Warning: c.Warning, Success: c.Success,
		Selection: c.Selection, Focus: c.Focus,
		Scrollbar: c.BorderStrong, ScrollbarWidth: 6,
		Radius: RadiusSM, Spacing: 4, FontSize: TextBase,
		Font: r.Font,
	}
}

// Radix Gray surfaces and text; Green/Amber/Red 11 carry status.
// White is the light content surface; spacing and typography follow design.html.
func lightColors() Colors {
	colors := Colors{
		Page: ui.Hex("#F9F9F9"), Bg: ui.Hex("#FFFFFF"), Panel: ui.Hex("#FCFCFC"),
		Fg: ui.Hex("#202020"), Muted: ui.Hex("#646464"), Faint: ui.Hex("#646464"),
		Border: ui.Hex("#E8E8E8"), BorderStrong: ui.Hex("#CECECE"),
		Primary: ui.Hex("#202020"), OnPrimary: ui.Hex("#FCFCFC"),
		Selection: ui.Hex("#E8E8E8"), Hover: ui.Hex("#F0F0F0"), Pressed: ui.Hex("#E0E0E0"),
		PrimaryHover: ui.Hex("#646464"), PrimaryPressed: ui.Hex("#646464"), Focus: ui.Hex("#646464"),
		Success: ui.Hex("#218358"), Warning: ui.Hex("#AB6400"), Danger: ui.Hex("#CE2C31"),
	}
	colors.Brand = colors.Primary
	return colors
}
func darkColors() Colors {
	colors := Colors{
		Page: ui.Hex("#111111"), Bg: ui.Hex("#191919"), Panel: ui.Hex("#111111"),
		Fg: ui.Hex("#EEEEEE"), Muted: ui.Hex("#B4B4B4"), Faint: ui.Hex("#B4B4B4"),
		Border: ui.Hex("#3A3A3A"), BorderStrong: ui.Hex("#484848"),
		Primary: ui.Hex("#EEEEEE"), OnPrimary: ui.Hex("#202020"),
		Selection: ui.Hex("#2A2A2A"), Hover: ui.Hex("#222222"), Pressed: ui.Hex("#313131"),
		PrimaryHover: ui.Hex("#B4B4B4"), PrimaryPressed: ui.Hex("#B4B4B4"), Focus: ui.Hex("#B4B4B4"),
		Success: ui.Hex("#3DD68C"), Warning: ui.Hex("#FFCA16"), Danger: ui.Hex("#FF9592"),
	}
	colors.Brand = colors.Primary
	return colors
}
