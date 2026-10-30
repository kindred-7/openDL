// Package signature verifies publisher signatures independently of file hashes.
package signature

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"

	"openDL/i18n"
)

// PublicKey decodes a pinned Ed25519 public key; it must come from a trusted
// configuration, never from the same untrusted response as the signature.
func PublicKey(text string) (ed25519.PublicKey, error) {
	if len(text) > 64<<10 {
		return nil, fmt.Errorf(i18n.T("签名公钥必须是 Base64 编码的 Ed25519 公钥"))
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf(i18n.T("签名公钥必须是 Base64 编码的 Ed25519 公钥"))
	}
	return ed25519.PublicKey(key), nil
}

func Verify(key ed25519.PublicKey, message []byte, encodedSignature string) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedSignature))
	if len(key) != ed25519.PublicKeySize || err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, message, sig) {
		return fmt.Errorf(i18n.T("数字签名无效或内容已被修改"))
	}
	return nil
}
