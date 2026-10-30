//go:build !windows && !(gssapi && cgo && (linux || darwin))

package winauth

import "openDL/i18n"

const Available = false

func New(spn string, kerberosOnly, rawNTLM bool, channelBinding []byte) (Session, error) {
	return nil, i18n.NewError("当前版本未启用系统集成认证")
}
