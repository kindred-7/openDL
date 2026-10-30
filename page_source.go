package main

import (
	"encoding/base64"
	"fmt"
	"net/textproto"
	"strings"

	"openDL/style"

	"github.com/egoist/mygo/ui"
)

func (pg *mainPage) openSource(row d1Row) {
	if pg.mgr == nil {
		return
	}
	req, ok := pg.mgr.Request(row.id)
	if !ok {
		return
	}
	pg.sourceJob++
	pg.sourceID, pg.sourceURL, pg.sourceOriginalURL = row.id, req.URL, req.URL
	pg.sourceSHA256 = req.SHA256
	pg.sourceSignatureKey, pg.sourceSignature = req.SignatureKey, req.Signature
	pg.sourceUser, pg.sourcePassword, pg.sourceHeaders, pg.sourceError = "", "", "", ""
	pg.sourceSaving = false
	pg.showSource = true
	pg.focus = "source-url"
}

func (pg *mainPage) sourceDialog(c *ui.Context, p *d1Palette) {
	ui.DialogBase(c, &pg.showSource, func(back, panel ui.Element) {
		_, h := c.Size()
		back.Background(ui.RGBA(0, 0, 0, .42)).Center()
		panel.Width(540).MaxHeight(h-48).Padding(18).Radius(8).Border(1, p.bd2).Background(p.bg).
			Label(tr("更新下载信息")).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, .14)).Children(func() {
			ui.Column(c).FillWidth().Gap(12).Children(func() {
				ui.Text(c, tr("更新下载信息")).FontSize(style.TextTitle).FontWeight(style.WeightMedium).TextColor(p.fg)
				ui.Text(c, tr("更换地址后将从头下载；原地址更新凭据可保留已完成分段。")).FontSize(style.TextSM).TextColor(p.mu)
				ui.Column(c).FillWidth().Gap(5).Children(func() {
					ui.Text(c, tr("下载地址")).FontSize(style.TextSM).TextColor(p.mu)
					c.Key("source-url")
					input := ui.TextInputBase(c, &pg.sourceURL).FillWidth().Height(30).Padding(0, 10).Radius(6).
						Border(1, p.bd2).Background(p.bg).TextColor(p.fg).FontSize(style.TextSM).Label(tr("下载地址")).FocusBind(&pg.focus, "source-url")
					if input.Focused() {
						input.BorderColor(p.pri)
					}
				})
				ui.Grid(c).FillWidth().Columns(2).Gap(12).Children(func() {
					d1Input(c, p, "用户名（可选）", "source-user", &pg.sourceUser, false)
					d1Input(c, p, "密码（可选）", "source-password", &pg.sourcePassword, true)
				})
				d1Input(c, p, "SHA-256（可选）", "source-sha256", &pg.sourceSHA256, false)
				ui.Column(c).FillWidth().Gap(5).Children(func() {
					ui.Text(c, tr("签名公钥（可选）")).FontSize(style.TextSM).TextColor(p.mu)
					c.Key("source-signature-key")
					ui.TextArea(c, &pg.sourceSignatureKey).Height(48).Padding(8, 10).FontSize(style.TextSM).
						Label(tr("签名公钥（可选）")).Background(p.bg).Border(1, p.bd2).Radius(6)
				})
				ui.Column(c).FillWidth().Gap(5).Children(func() {
					ui.Text(c, tr("分离签名（可选）")).FontSize(style.TextSM).TextColor(p.mu)
					c.Key("source-signature")
					ui.TextArea(c, &pg.sourceSignature).Height(48).Padding(8, 10).FontSize(style.TextSM).
						Label(tr("分离签名（可选）")).Background(p.bg).Border(1, p.bd2).Radius(6)
				})
				ui.Column(c).FillWidth().Gap(5).Children(func() {
					ui.Text(c, tr("额外请求头（每行 名称: 值）")).FontSize(style.TextSM).TextColor(p.mu)
					c.Key("source-headers")
					ui.TextArea(c, &pg.sourceHeaders).Height(76).Padding(8, 10).FontSize(style.TextSM).
						Label(tr("额外请求头")).Background(p.bg).Border(1, p.bd2).Radius(6)
				})
				if pg.sourceError != "" {
					ui.Text(c, tr(pg.sourceError)).Role(ui.RoleStatus).FontSize(style.TextSM).TextColor(p.bad)
				}
				ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
					d1Button(c, p, "取消", "").Disabled(pg.sourceSaving).OnClick(func() { pg.showSource = false })
					d1PrimaryButton(c, p, "保存", "", !pg.sourceSaving).OnClick(pg.saveSource)
				})
			})
		})
	})
}

func (pg *mainPage) saveSource() {
	if pg.sourceSaving || pg.mgr == nil {
		return
	}
	if pg.win == nil {
		pg.sourceError = tr("窗口尚未就绪")
		return
	}
	req, ok := pg.mgr.Request(pg.sourceID)
	if !ok {
		pg.sourceError = tr("任务不存在")
		return
	}
	address := strings.TrimSpace(pg.sourceURL)
	headers := req.Headers
	if address != pg.sourceOriginalURL {
		headers = nil
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	canonical := make(map[string]string, len(headers))
	for name, value := range headers {
		canonical[textproto.CanonicalMIMEHeaderKey(name)] = value
	}
	headers = canonical
	for _, line := range strings.Split(pg.sourceHeaders, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		if !ok || name == "" {
			pg.sourceError = tr("请求头应按“名称: 值”填写")
			return
		}
		headers[name] = strings.TrimSpace(value)
	}
	if pg.sourceUser != "" {
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(pg.sourceUser+":"+pg.sourcePassword))
	} else if pg.sourcePassword != "" {
		pg.sourceError = tr("请填写登录用户名")
		return
	}
	pg.sourceError, pg.sourceSaving = "", true
	pg.sourceJob++
	job, id, win, mgr, expected := pg.sourceJob, pg.sourceID, pg.win, pg.mgr, pg.sourceSHA256
	signatureKey, signatureText := pg.sourceSignatureKey, pg.sourceSignature
	go func() {
		changed, err := mgr.UpdateSourceWithSHA256(id, address, headers, expected, signatureKey, signatureText)
		win.Update(func() {
			if job != pg.sourceJob {
				return
			}
			pg.sourceSaving = false
			if err != nil {
				pg.sourceError = err.Error()
				return
			}
			pg.showSource = false
			pg.sourcePassword, pg.sourceHeaders = "", ""
			message := tr("下载信息已更新")
			if changed {
				message = fmt.Sprintf(tr("%s，继续时将从头下载"), message)
			}
			pg.flash(message)
		})
	}()
}
