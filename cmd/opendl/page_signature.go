package main

func signatureLabel(row d1Row) string {
	required := row.snap.SignatureRequired
	if row.pending {
		required = row.req.RequireSignature
	}
	if row.snap.SignatureVerified {
		return tr("上次数字签名验证通过")
	}
	if !required {
		return tr("未要求验证")
	}
	return tr("数字签名待验证")
}

func (pg *mainPage) verifyFileSignature(row d1Row) {
	if pg.win == nil || pg.mgr == nil || row.pending {
		return
	}
	win, mgr, id := pg.win, pg.mgr, row.id
	go func() {
		err := mgr.VerifySignature(id)
		win.Update(func() {
			select {
			case <-pg.stop:
				return
			default:
			}
			if err != nil {
				pg.flash(err.Error())
			} else {
				pg.flash(tr("文件数字签名已通过验证"))
			}
		})
	}()
}
