package style

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// AccentScales 使用 Radix Themes 3.0.2 的 sRGB 色阶（3、8、9、10、11）。
type AccentScale struct {
	ID, Label   string
	Light, Dark [5]string
}

var AccentScales = []AccentScale{
	{"tomato", "番茄红", [5]string{"#feebe7", "#ec8e7b", "#e54d2e", "#dd4425", "#d13415"}, [5]string{"#391714", "#ac4d39", "#e54d2e", "#ec6142", "#ff977d"}},
	{"red", "红色", [5]string{"#feebec", "#eb8e90", "#e5484d", "#dc3e42", "#ce2c31"}, [5]string{"#3b1219", "#b54548", "#e5484d", "#ec5d5e", "#ff9592"}},
	{"ruby", "宝石红", [5]string{"#feeaed", "#e592a3", "#e54666", "#dc3b5d", "#ca244d"}, [5]string{"#3a141e", "#b3445a", "#e54666", "#ec5a72", "#ff949d"}},
	{"crimson", "深红", [5]string{"#ffe9f0", "#e093b2", "#e93d82", "#df3478", "#cb1d63"}, [5]string{"#381525", "#b0436e", "#e93d82", "#ee518a", "#ff92ad"}},
	{"pink", "粉色", [5]string{"#fee9f5", "#dd93c2", "#d6409f", "#cf3897", "#c2298a"}, [5]string{"#37172f", "#a84885", "#d6409f", "#de51a8", "#ff8dcc"}},
	{"plum", "梅红", [5]string{"#fbebfb", "#cf91d8", "#ab4aba", "#a144af", "#953ea3"}, [5]string{"#351a35", "#92549c", "#ab4aba", "#b658c4", "#e796f3"}},
	{"purple", "紫色", [5]string{"#f7edfe", "#be93e4", "#8e4ec6", "#8347b9", "#8145b5"}, [5]string{"#301c3b", "#8457aa", "#8e4ec6", "#9a5cd0", "#d19dff"}},
	{"violet", "紫罗兰", [5]string{"#f4f0fe", "#aa99ec", "#6e56cf", "#654dc4", "#6550b9"}, [5]string{"#291f43", "#6958ad", "#6e56cf", "#7d66d9", "#baa7ff"}},
	{"iris", "鸢尾紫", [5]string{"#f0f1fe", "#9b9ef0", "#5b5bd6", "#5151cd", "#5753c6"}, [5]string{"#202248", "#5958b1", "#5b5bd6", "#6e6ade", "#b1a9ff"}},
	{"indigo", "靛蓝", [5]string{"#edf2fe", "#8da4ef", "#3e63dd", "#3358d4", "#3a5bc7"}, [5]string{"#182449", "#435db1", "#3e63dd", "#5472e4", "#9eb1ff"}},
	{"blue", "蓝色", [5]string{"#e6f4fe", "#5eb1ef", "#0090ff", "#0588f0", "#0d74ce"}, [5]string{"#0d2847", "#2870bd", "#0090ff", "#3b9eff", "#70b8ff"}},
	{"cyan", "青色", [5]string{"#def7f9", "#3db9cf", "#00a2c7", "#0797b9", "#107d98"}, [5]string{"#082c36", "#11809c", "#00a2c7", "#23afd0", "#4ccce6"}},
	{"teal", "蓝绿", [5]string{"#e0f8f3", "#53b9ab", "#12a594", "#0d9b8a", "#008573"}, [5]string{"#0d2d2a", "#207e73", "#12a594", "#0eb39e", "#0bd8b6"}},
	{"jade", "翡翠绿", [5]string{"#e6f7ed", "#56ba9f", "#29a383", "#26997b", "#208368"}, [5]string{"#0f2e22", "#2a7e68", "#29a383", "#27b08b", "#1fd8a4"}},
	{"green", "绿色", [5]string{"#e6f6eb", "#5bb98b", "#30a46c", "#2b9a66", "#218358"}, [5]string{"#132d21", "#2f7c57", "#30a46c", "#33b074", "#3dd68c"}},
	{"grass", "草绿", [5]string{"#e9f6e9", "#65ba74", "#46a758", "#3e9b4f", "#2a7e3b"}, [5]string{"#1b2a1e", "#3e7949", "#46a758", "#53b365", "#71d083"}},
	{"lime", "青柠", [5]string{"#eef6d6", "#8db654", "#bdee63", "#b0e64c", "#5c7c2f"}, [5]string{"#1f2917", "#577538", "#bdee63", "#d4ff70", "#bde56c"}},
	{"mint", "薄荷绿", [5]string{"#ddf9f2", "#4cbba5", "#86ead4", "#7de0cb", "#027864"}, [5]string{"#092c2b", "#277f70", "#86ead4", "#a8f5e5", "#58d5ba"}},
	{"sky", "天蓝", [5]string{"#e1f6fd", "#60b3d7", "#7ce2fe", "#74daf8", "#00749e"}, [5]string{"#112840", "#197cae", "#7ce2fe", "#a8eeff", "#75c7f0"}},
	{"yellow", "黄色", [5]string{"#fffab8", "#d5ae39", "#ffe629", "#ffdc00", "#9e6c00"}, [5]string{"#2d2305", "#836a21", "#ffe629", "#ffff57", "#f5e147"}},
	{"amber", "琥珀", [5]string{"#fff7c2", "#e2a336", "#ffc53d", "#ffba18", "#ab6400"}, [5]string{"#302008", "#8f6424", "#ffc53d", "#ffd60a", "#ffca16"}},
	{"orange", "橙色", [5]string{"#ffefd6", "#ec9455", "#f76b15", "#ef5f00", "#cc4e00"}, [5]string{"#331e0b", "#a35829", "#f76b15", "#ff801f", "#ffa057"}},
	{"brown", "棕色", [5]string{"#f6eee7", "#cea37e", "#ad7f58", "#a07553", "#815e46"}, [5]string{"#28211d", "#7c5f46", "#ad7f58", "#b88c67", "#dbb594"}},
	{"bronze", "古铜", [5]string{"#f6edea", "#c2a499", "#a18072", "#957468", "#7d5e54"}, [5]string{"#262220", "#6f5f58", "#a18072", "#ae8c7e", "#d4b3a5"}},
	{"gold", "金色", [5]string{"#f2f0e7", "#b9a88d", "#978365", "#8c7a5e", "#71624b"}, [5]string{"#24231f", "#696256", "#978365", "#a39073", "#cbb99f"}},
}

func Accent(id string) (AccentScale, bool) {
	for _, scale := range AccentScales {
		if scale.ID == id {
			return scale, true
		}
	}
	return AccentScale{}, false
}

// NewAccentRules 将强调色映射到亮暗主题的语义令牌；空值使用默认蓝色。
func NewAccentRules(dark bool, id string) *Rules {
	rules := NewRules(dark)
	if id == "" {
		id = "blue"
	}
	scale, ok := Accent(id)
	if !ok {
		return rules
	}
	values := scale.Light
	if dark {
		values = scale.Dark
	}
	c := &rules.Colors
	// 选中区域保持轻微的主题色倾向，避免大面积底色与实心控件争夺注意力。
	c.Selection = c.Selection.Mix(ui.Hex(values[0]), 0.5)
	c.Focus = ui.Hex(values[1])
	c.Primary = ui.Hex(values[2])
	c.PrimaryHover = ui.Hex(values[3])
	c.PrimaryPressed = ui.Hex(values[3])
	c.Brand = ui.Hex(values[4])
	// Radix 的五种浅亮色采用深色前景；其余色调统一使用白色前景。
	// 色阶 9、10 如果不足以衬托白字，就适度压暗实心色，保留原有色相。
	switch id {
	case "sky", "mint", "lime", "yellow", "amber":
		c.OnPrimary = ui.Hex("#202020")
	default:
		c.OnPrimary = ui.Hex("#FFFFFF")
		if !dark && luminance(ui.Hex(values[4])) <= 1.05/4.6-0.05 {
			c.Primary = ui.Hex(values[4])
			c.PrimaryHover = c.Primary.Mix(ui.Hex("#000000"), 0.10)
		} else {
			c.Primary = solidForWhite(c.Primary)
			c.PrimaryHover = solidForWhite(c.PrimaryHover)
		}
		c.PrimaryPressed = c.PrimaryHover.Mix(ui.Hex("#000000"), 0.08)
	}
	return rules
}

func solidForWhite(color ui.Color) ui.Color {
	for amount := float32(0); amount <= 1; amount += 0.01 {
		candidate := color.Mix(ui.Hex("#000000"), amount)
		if luminance(candidate) <= 1.05/4.6-0.05 {
			return candidate
		}
	}
	return ui.Hex("#000000")
}

func luminance(color ui.Color) float64 {
	var v [3]float64
	for i, n := range [3]uint8{color.R, color.G, color.B} {
		x := float64(n) / 255
		if x <= 0.04045 {
			v[i] = x / 12.92
		} else {
			v[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*v[0] + 0.7152*v[1] + 0.0722*v[2]
}
