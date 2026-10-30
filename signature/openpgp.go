package signature

// OpenPGP 分离签名验证：只接受 v4 公钥与 v4 分离签名，支持算法 1（RSA，
// PKCS#1 v1.5）、19（ECDSA）与 22（EdDSA）。分段长度数据包与其余算法拒绝。
// v4 签名
// 名（类型 0x00，哈希为 SHA-2 系）。摘要 = H(文件内容 || 签名哈希区 ||
// 尾部)，验证使用标准库 ed25519；其余算法与封装明确拒绝，不做静默降级。

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"hash"
	"io"
	"strings"

	"openDL/i18n"
)

var errOpenPGPUnsupported = i18n.NewError("仅支持 OpenPGP 的 RSA/ECDSA/EdDSA v4 分离签名")

type openpgpPacket struct {
	tag  int
	body []byte
}

// openpgpPackets 解析新旧两种封装的 OpenPGP 数据包序列。
func openpgpPackets(data []byte) ([]openpgpPacket, error) {
	var packets []openpgpPacket
	for len(data) > 0 {
		if data[0]&0x80 == 0 {
			return nil, errOpenPGPUnsupported
		}
		tag, header, length := 0, 0, 0
		if data[0]&0x40 != 0 {
			tag = int(data[0] & 0x3f)
			header = 1
			if len(data) < 2 {
				return nil, errOpenPGPUnsupported
			}
			switch first := data[1]; {
			case first < 192:
				length, header = int(first), 2
			case first < 224:
				if len(data) < 3 {
					return nil, errOpenPGPUnsupported
				}
				length, header = (int(first)-192)<<8+int(data[2])+192, 3
			case first == 255:
				if len(data) < 6 {
					return nil, errOpenPGPUnsupported
				}
				length, header = int(binary.BigEndian.Uint32(data[2:6])), 6
			default:
				// 分段长度只用于流式数据包，不适用于密钥与签名。
				return nil, errOpenPGPUnsupported
			}
		} else {
			tag = int((data[0] >> 2) & 0x0f)
			if len(data) < 2 {
				return nil, errOpenPGPUnsupported
			}
			switch data[0] & 0x03 {
			case 0:
				length, header = int(data[1]), 2
			case 1:
				if len(data) < 3 {
					return nil, errOpenPGPUnsupported
				}
				length, header = int(data[1])<<8|int(data[2]), 3
			case 2:
				if len(data) < 5 {
					return nil, errOpenPGPUnsupported
				}
				length, header = int(binary.BigEndian.Uint32(data[1:5])), 5
			default:
				return nil, errOpenPGPUnsupported
			}
		}
		if length < 0 || header+length > len(data) {
			return nil, errOpenPGPUnsupported
		}
		packets = append(packets, openpgpPacket{tag: tag, body: data[header : header+length]})
		data = data[header+length:]
	}
	return packets, nil
}

// openpgpDearmor 提取装甲块的负载；未装甲数据原样返回。
func openpgpDearmor(data []byte, block string) ([]byte, error) {
	text := string(data)
	begin := "-----BEGIN " + block + "-----"
	if !strings.Contains(text, begin) {
		return data, nil
	}
	start := strings.Index(text, begin)
	rest := text[start+len(begin):]
	end := "-----END " + block + "-----"
	stop := strings.Index(rest, end)
	if stop < 0 {
		return nil, errOpenPGPUnsupported
	}
	var out strings.Builder
	for _, line := range strings.Split(rest[:stop], "\n") {
		line = strings.TrimSpace(line)
		// 头部行含冒号或空格，校验和行以等号开头；负载行只含 Base64 字符。
		if line == "" || strings.HasPrefix(line, "=") || strings.ContainsAny(line, ": ") {
			continue
		}
		out.WriteString(line)
	}
	decoded, err := base64.StdEncoding.DecodeString(out.String())
	if err != nil {
		return nil, errOpenPGPUnsupported
	}
	return decoded, nil
}

// openpgpEd25519Key 从公钥数据包负载提取 Ed25519 公钥。
func openpgpEd25519Key(body []byte) (ed25519.PublicKey, error) {
	if len(body) < 10 || body[0] != 4 || body[5] != 22 {
		return nil, errOpenPGPUnsupported
	}
	rest := body[6:]
	if rest[0] != 3 || rest[1] != 0x2b || rest[2] != 0x65 || rest[3] != 0x70 {
		return nil, errOpenPGPUnsupported
	}
	point := rest[4:]
	if len(point) < 2 {
		return nil, errOpenPGPUnsupported
	}
	bits := int(point[0])<<8 | int(point[1])
	if bits <= 0 || bits > 264 {
		return nil, errOpenPGPUnsupported
	}
	size := (bits + 7) / 8
	if size != 33 || len(point) < 2+size || point[2] != 0x40 {
		return nil, errOpenPGPUnsupported
	}
	key := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(key, point[3:35])
	return key, nil
}

// openpgpSubpackets 检查子包封装，并拒绝尚未实现的关键约束和有效期。
// 非哈希区不可信，但同样不能忽略其中声明的关键子包。
func openpgpSubpackets(data []byte) bool {
	for len(data) > 0 {
		var size uint64
		var header int
		switch first := data[0]; {
		case first < 192:
			size, header = uint64(first), 1
		case first < 255:
			if len(data) < 2 {
				return false
			}
			size, header = uint64((int(first)-192)<<8+int(data[1])+192), 2
		default:
			if len(data) < 5 {
				return false
			}
			size, header = uint64(binary.BigEndian.Uint32(data[1:5])), 5
		}
		if size == 0 || size > uint64(len(data)-header) {
			return false
		}
		typ := data[header]
		if typ&0x80 != 0 || typ == 3 {
			return false
		}
		data = data[header+int(size):]
	}
	return true
}

// openpgpEd25519Signature 从签名数据包负载提取哈希算法、哈希区与签名值。
func openpgpEd25519Signature(body []byte) (int, []byte, [ed25519.SignatureSize]byte, [2]byte, error) {
	var sig [ed25519.SignatureSize]byte
	var prefix [2]byte
	if len(body) < 6 || body[0] != 4 || body[1] != 0x00 || body[2] != 22 {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	hashAlgo := int(body[3])
	hashedLen := int(body[4])<<8 | int(body[5])
	if 6+hashedLen+2 > len(body) {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	hashed := body[:6+hashedLen]
	if !openpgpSubpackets(body[6 : 6+hashedLen]) {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	rest := body[6+hashedLen:]
	unhashedLen := int(rest[0])<<8 | int(rest[1])
	rest = rest[2:]
	if unhashedLen > len(rest) {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	if !openpgpSubpackets(rest[:unhashedLen]) {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	rest = rest[unhashedLen:]
	if len(rest) < 2 {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	prefix[0], prefix[1] = rest[0], rest[1]
	rest = rest[2:]
	r, rest, ok := openpgpMPI(rest, 256)
	if !ok {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	s, rest, ok := openpgpMPI(rest, 256)
	if !ok || len(rest) != 0 {
		return 0, nil, sig, prefix, errOpenPGPUnsupported
	}
	copy(sig[:32], r)
	copy(sig[32:], s)
	return hashAlgo, hashed, sig, prefix, nil
}

// openpgpMPI 读取一个不超过上限位数的 MPI 整数，左补零到定长。
func openpgpMPI(data []byte, maxBits int) ([]byte, []byte, bool) {
	if len(data) < 2 {
		return nil, nil, false
	}
	bits := int(data[0])<<8 | int(data[1])
	if bits <= 0 || bits > maxBits {
		return nil, nil, false
	}
	size := (bits + 7) / 8
	if len(data) < 2+size {
		return nil, nil, false
	}
	value := make([]byte, maxBits/8)
	copy(value[maxBits/8-size:], data[2:2+size])
	return value, data[2+size:], true
}

// openpgpDigest 计算 v4 签名摘要：H(文件内容 || 哈希区 || 尾部)。
func openpgpDigest(hashAlgo int, hashed []byte, content io.Reader) ([]byte, error) {

	var engine hash.Hash
	switch hashAlgo {
	case 8:
		engine = sha256.New()
	case 9:
		engine = sha512.New384()
	case 10:
		engine = sha512.New()
	default:
		return nil, errOpenPGPUnsupported
	}
	trailer := make([]byte, 6)
	trailer[0], trailer[1] = 0x04, 0xff
	binary.BigEndian.PutUint32(trailer[2:], uint32(len(hashed)))
	if _, err := io.Copy(engine, content); err != nil {
		return nil, err
	}
	if _, err := engine.Write(hashed); err != nil {
		return nil, err
	}
	if _, err := engine.Write(trailer); err != nil {
		return nil, err
	}
	return engine.Sum(nil), nil
}

// CheckOpenPGPKey 报告数据是否为 OpenPGP EdDSA 公钥（装甲或二进制）。
func CheckOpenPGPKey(data []byte) error {
	decoded, err := openpgpDearmor(data, "PGP PUBLIC KEY BLOCK")
	if err != nil {
		return err
	}
	packets, err := openpgpPackets(decoded)
	if err != nil {
		return err
	}
	for _, packet := range packets {
		if packet.tag == 6 {
			if _, _, err := openpgpPublicKey(packet.body); err == nil {
				return nil
			}
		}
	}
	return errOpenPGPUnsupported
}

// CheckOpenPGPSignature 报告数据是否为 OpenPGP 分离签名（装甲或二进
// 制）。
func CheckOpenPGPSignature(data []byte) error {
	decoded, err := openpgpDearmor(data, "PGP SIGNATURE")
	if err != nil {
		return err
	}
	packets, err := openpgpPackets(decoded)
	if err != nil {
		return err
	}
	for _, packet := range packets {
		if packet.tag != 2 {
			continue
		}
		if _, _, _, _, err := openpgpSignature(packet.body); err == nil {
			return nil
		}
	}
	return errOpenPGPUnsupported
}

// VerifyOpenPGP 校验 OpenPGP 分离签名（RSA/ECDSA/EdDSA）；公钥与签名均接
// 受装甲或二进制封装。其余算法、签名类型与封装明确拒绝。
func VerifyOpenPGP(publicKey, detached []byte, content io.ReadSeeker) error {
	keyData, err := openpgpDearmor(publicKey, "PGP PUBLIC KEY BLOCK")
	if err != nil {
		return err
	}
	sigData, err := openpgpDearmor(detached, "PGP SIGNATURE")
	if err != nil {
		return err
	}
	keyPackets, err := openpgpPackets(keyData)
	if err != nil {
		return err
	}
	var key any
	keyAlgo := 0
	for _, packet := range keyPackets {
		if packet.tag == 6 {
			if key, keyAlgo, err = openpgpPublicKey(packet.body); err == nil {
				break
			}
		}
	}
	if key == nil {
		return errOpenPGPUnsupported
	}
	sigPackets, err := openpgpPackets(sigData)
	if err != nil {
		return err
	}
	if content == nil {
		return errOpenPGPUnsupported
	}
	matched := false
	for _, packet := range sigPackets {
		if packet.tag != 2 {
			continue
		}
		hashAlgo, hashed, parts, prefix, err := openpgpSignature(packet.body)
		if err != nil {
			continue
		}
		if _, err := content.Seek(0, io.SeekStart); err != nil {
			return err
		}
		digest, err := openpgpDigest(hashAlgo, hashed, content)
		if err != nil {
			return err
		}
		if !bytes.Equal(digest[:2], prefix[:]) {
			continue
		}
		matched = true
		if verifyOpenPGPValue(key, keyAlgo, hashAlgo, digest, parts) {
			return nil
		}
	}
	if matched {
		return i18n.NewError("OpenPGP 签名校验失败")
	}
	return errOpenPGPUnsupported
}
