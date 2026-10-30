package signature

// Minisign 分离签名验证：支持预哈希（Ed，Blake2b-512）与旧版（ED，对原文
// 签名，整体读入上限 64 MiB）两种格式。公钥接受 Base64 负载或公钥文件全文；
// 校验密钥编号、文件签名与可信注释的全局签名。可信注释限制为有效 UTF-8，
// 拒绝 NUL、非制表符控制字符及过长内容。

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/blake2b"

	"openDL/i18n"
)

const minisignTrustedLimit = 512

// 旧版 ED 直接对原文签名，验证需整体读入内存；超过上限明确拒绝。
const minisignLegacyLimit = 64 << 20

var errMinisignFormat = i18n.NewError("Minisign 公钥或分离签名格式无效")

// minisignLines 拆出非空行；注释行在结构解析中按位置识别。
func minisignLines(value string) []string {
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// minisignDecode 尝试把一行按 Base64 解码为定长负载；带填充与无填充两种
// 写法都接受。
func minisignDecode(line string, size int) ([]byte, bool) {
	line = strings.TrimSpace(line)
	if decoded, err := base64.StdEncoding.DecodeString(line); err == nil && len(decoded) == size {
		return decoded, true
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(line, "=")); err == nil && len(decoded) == size {
		return decoded, true
	}
	return nil, false
}

// parseMinisignKey 解析公钥负载：算法（2）+ 密钥标识（8）+ Ed25519 公钥（32）。
func parseMinisignKey(value string) ([]byte, []byte, error) {
	for _, line := range minisignLines(value) {
		payload, ok := minisignDecode(line, 42)
		if !ok {
			continue
		}
		if string(payload[:2]) != "Ed" {
			return nil, nil, errMinisignFormat
		}
		key := make([]byte, ed25519.PublicKeySize)
		copy(key, payload[10:42])
		return key, append([]byte(nil), payload[2:10]...), nil
	}
	return nil, nil, errMinisignFormat
}

// parseMinisignSignature 解析签名负载：算法（2）+ 密钥标识（8）+ 签名（64），
// 以及可选的可信注释与全局签名。
func parseMinisignSignature(value string) (string, []byte, []byte, string, []byte, error) {
	lines := minisignLines(value)
	alg := ""
	var id, sig, globalSig []byte
	trusted := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if len(line) >= 16 && strings.EqualFold(line[:16], "trusted comment:") {
			trusted = strings.TrimSpace(line[16:])
			if i+1 < len(lines) {
				if decoded, ok := minisignDecode(lines[i+1], ed25519.SignatureSize); ok {
					globalSig = decoded
				}
			}
			continue
		}
		if sig != nil {
			continue
		}
		payload, ok := minisignDecode(line, 74)
		if !ok {
			continue
		}
		alg = string(payload[:2])
		if alg != "Ed" && alg != "ED" {
			return "", nil, nil, "", nil, errMinisignFormat
		}
		id = append([]byte(nil), payload[2:10]...)
		sig = append([]byte(nil), payload[10:74]...)
	}
	if sig == nil {
		return "", nil, nil, "", nil, errMinisignFormat
	}
	return alg, id, sig, trusted, globalSig, nil
}

// validTrustedComment 限制可信注释：有效 UTF-8、无 NUL、除制表符外无控制
// 字符、长度有界。
func validTrustedComment(comment string) bool {
	if comment == "" || len(comment) > minisignTrustedLimit || !utf8.ValidString(comment) {
		return false
	}
	for _, r := range comment {
		if r == 0 || (r < 0x20 && r != '\t') || r == 0x7f {
			return false
		}
	}
	return true
}

// CheckMinisignKey 报告文本是否为 Minisign 公钥。
func CheckMinisignKey(value string) error {
	_, _, err := parseMinisignKey(value)
	return err
}

// CheckMinisignSignature 报告文本是否为 Minisign 预哈希分离签名。
func CheckMinisignSignature(value string) error {
	_, _, _, _, _, err := parseMinisignSignature(value)
	return err
}

// VerifyMinisign 校验 Minisign 预哈希分离签名：文件的 Blake2b-512 摘要与
// "Ed" 前缀共同作为签名消息；可信注释存在时必须提供并通过全局签名校验。
func VerifyMinisign(publicKey, detached string, content io.ReadSeeker) error {
	key, keyID, err := parseMinisignKey(publicKey)
	if err != nil {
		return err
	}
	alg, id, sig, trusted, globalSig, err := parseMinisignSignature(detached)
	if err != nil {
		return err
	}
	if !bytes.Equal(keyID, id) {
		return errMinisignFormat
	}
	if content == nil {
		return errMinisignFormat
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var message []byte
	switch alg {
	case "Ed":
		digest, err := blake2b.New512(nil)
		if err != nil {
			return err
		}
		if _, err := io.Copy(digest, content); err != nil {
			return err
		}
		message = append([]byte("Ed"), digest.Sum(nil)...)
	case "ED":
		// 旧版直接对原文签名；整体读入，超过上限明确拒绝。
		data, err := io.ReadAll(io.LimitReader(content, minisignLegacyLimit+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > minisignLegacyLimit {
			return fmt.Errorf(i18n.T("Minisign ED 格式需要读入全文，文件不能超过 %d MiB"), minisignLegacyLimit>>20)
		}
		message = append([]byte("ED"), data...)
	default:
		return errMinisignFormat
	}
	if !ed25519.Verify(ed25519.PublicKey(key), message, sig) {
		return i18n.NewError("Minisign 签名校验失败")
	}
	if trusted != "" {
		if !validTrustedComment(trusted) {
			return i18n.NewError("Minisign 可信注释无效")
		}
		if globalSig == nil {
			return i18n.NewError("Minisign 可信注释缺少全局签名")
		}
		global := append(append([]byte(nil), sig...), trusted...)
		if !ed25519.Verify(ed25519.PublicKey(key), global, globalSig) {
			return i18n.NewError("Minisign 可信注释校验失败")
		}
	}
	return nil
}
