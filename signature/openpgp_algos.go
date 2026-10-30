package signature

// OpenPGP 签名算法支持：v4 签名包的 RSA（1，PKCS#1 v1.5）、ECDSA（19）与
// EdDSA（22）；公钥与签名负载的通用解析与验证在此，摘要构造沿用 v4 规范。

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"math/big"
)

// openpgpPublicKey 解析 v4 公钥负载，返回公钥对象与算法号。
func openpgpPublicKey(body []byte) (any, int, error) {
	if len(body) < 6 || body[0] != 4 {
		return nil, 0, errOpenPGPUnsupported
	}
	algo := int(body[5])
	rest := body[6:]
	switch algo {
	case 22:
		if len(rest) < 4 || rest[0] != 3 || rest[1] != 0x2b || rest[2] != 0x65 || rest[3] != 0x70 {
			return nil, 0, errOpenPGPUnsupported
		}
		point := rest[4:]
		if len(point) < 2 {
			return nil, 0, errOpenPGPUnsupported
		}
		bits := int(point[0])<<8 | int(point[1])
		size := (bits + 7) / 8
		if bits <= 0 || size != 33 || len(point) < 2+size || point[2] != 0x40 {
			return nil, 0, errOpenPGPUnsupported
		}
		key := make(ed25519.PublicKey, ed25519.PublicKeySize)
		copy(key, point[3:35])
		return key, algo, nil
	case 1:
		n, rest, ok := openpgpMPIRaw(rest, 1<<17)
		if !ok {
			return nil, 0, errOpenPGPUnsupported
		}
		e, _, ok := openpgpMPIRaw(rest, 32)
		if !ok {
			return nil, 0, errOpenPGPUnsupported
		}
		modulus := new(big.Int).SetBytes(n)
		exponent := new(big.Int).SetBytes(e)
		if modulus.BitLen() < 1024 || modulus.BitLen() > 16384 || !exponent.IsInt64() || exponent.Int64() < 3 {
			return nil, 0, errOpenPGPUnsupported
		}
		return &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}, algo, nil
	case 19:
		if len(rest) < 1 {
			return nil, 0, errOpenPGPUnsupported
		}
		oidLen := int(rest[0])
		if len(rest) < 1+oidLen {
			return nil, 0, errOpenPGPUnsupported
		}
		curve := openpgpCurve(rest[1 : 1+oidLen])
		if curve == nil {
			return nil, 0, errOpenPGPUnsupported
		}
		point := rest[1+oidLen:]
		if len(point) < 2 {
			return nil, 0, errOpenPGPUnsupported
		}
		bits := int(point[0])<<8 | int(point[1])
		size := (bits + 7) / 8
		if bits <= 0 || len(point) < 2+size {
			return nil, 0, errOpenPGPUnsupported
		}
		x, y := elliptic.Unmarshal(curve, point[2:2+size])
		if x == nil {
			return nil, 0, errOpenPGPUnsupported
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, algo, nil
	}
	return nil, 0, errOpenPGPUnsupported
}

// openpgpCurve 按 OID 返回 ECDSA 曲线（P-256/P-384/P-521）。
func openpgpCurve(oid []byte) elliptic.Curve {
	if len(oid) == 8 && oid[0] == 0x2a && oid[1] == 0x86 && oid[2] == 0x48 && oid[3] == 0xce && oid[4] == 0x3d && oid[5] == 0x03 && oid[6] == 0x01 && oid[7] == 0x07 {
		return elliptic.P256()
	}
	if len(oid) == 5 && oid[0] == 0x2b && oid[1] == 0x81 && oid[2] == 0x04 && oid[3] == 0x00 && oid[4] == 0x22 {
		return elliptic.P384()
	}
	if len(oid) == 5 && oid[0] == 0x2b && oid[1] == 0x81 && oid[2] == 0x04 && oid[3] == 0x00 && oid[4] == 0x23 {
		return elliptic.P521()
	}
	return nil
}

// openpgpMPIRaw 读取一个不超过上限位数的 MPI，返回去掉前导零的大端值。
func openpgpMPIRaw(data []byte, maxBits int) ([]byte, []byte, bool) {
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
	value := data[2 : 2+size]
	for len(value) > 1 && value[0] == 0 {
		value = value[1:]
	}
	return value, data[2+size:], true
}

// openpgpSignature 解析 v4 签名负载：哈希算法、哈希区、签名分量与前缀。
func openpgpSignature(body []byte) (int, []byte, [][]byte, [2]byte, error) {
	var prefix [2]byte
	if len(body) < 6 || body[0] != 4 || body[1] != 0x00 {
		return 0, nil, nil, prefix, errOpenPGPUnsupported
	}
	algo := int(body[2])
	hashAlgo := int(body[3])
	hashedLen := int(body[4])<<8 | int(body[5])
	if 6+hashedLen+2 > len(body) {
		return 0, nil, nil, prefix, errOpenPGPUnsupported
	}
	hashed := body[:6+hashedLen]
	rest := body[6+hashedLen:]
	unhashedLen := int(rest[0])<<8 | int(rest[1])
	rest = rest[2:]
	if unhashedLen > len(rest) {
		return 0, nil, nil, prefix, errOpenPGPUnsupported
	}
	rest = rest[unhashedLen:]
	if len(rest) < 2 {
		return 0, nil, nil, prefix, errOpenPGPUnsupported
	}
	prefix[0], prefix[1] = rest[0], rest[1]
	rest = rest[2:]
	switch algo {
	case 22:
		r, rest2, ok := openpgpMPI(rest, 256)
		if !ok {
			return 0, nil, nil, prefix, errOpenPGPUnsupported
		}
		s, _, ok := openpgpMPI(rest2, 256)
		if !ok {
			return 0, nil, nil, prefix, errOpenPGPUnsupported
		}
		return hashAlgo, hashed, [][]byte{r, s}, prefix, nil
	case 19:
		r, rest2, ok := openpgpMPIRaw(rest, 528)
		if !ok {
			return 0, nil, nil, prefix, errOpenPGPUnsupported
		}
		s, _, ok := openpgpMPIRaw(rest2, 528)
		if !ok {
			return 0, nil, nil, prefix, errOpenPGPUnsupported
		}
		return hashAlgo, hashed, [][]byte{r, s}, prefix, nil
	case 1:
		sig, _, ok := openpgpMPIRaw(rest, 1<<17)
		if !ok {
			return 0, nil, nil, prefix, errOpenPGPUnsupported
		}
		return hashAlgo, hashed, [][]byte{sig}, prefix, nil
	}
	return 0, nil, nil, prefix, errOpenPGPUnsupported
}

// openpgpHashFunc 把 v4 签名的哈希算法号映射为 crypto.Hash；未支持的
// 算法返回 0。与 openpgpDigest 支持的算法保持一致（8/9/10）。
func openpgpHashFunc(hashAlgo int) crypto.Hash {
	switch hashAlgo {
	case 8:
		return crypto.SHA256
	case 9:
		return crypto.SHA384
	case 10:
		return crypto.SHA512
	}
	return 0
}

// verifyOpenPGPValue 按公钥算法验证摘要与签名分量。
func verifyOpenPGPValue(key any, algo, hashAlgo int, digest []byte, parts [][]byte) bool {
	switch algo {
	case 22:
		public, ok := key.(ed25519.PublicKey)
		if !ok || len(parts) != 2 {
			return false
		}
		sig := append(append([]byte(nil), parts[0]...), parts[1]...)
		if len(sig) != ed25519.SignatureSize {
			return false
		}
		return ed25519.Verify(public, digest, sig)
	case 19:
		public, ok := key.(*ecdsa.PublicKey)
		if !ok || len(parts) != 2 {
			return false
		}
		return ecdsa.Verify(public, digest, new(big.Int).SetBytes(parts[0]), new(big.Int).SetBytes(parts[1]))
	case 1:
		public, ok := key.(*rsa.PublicKey)
		if !ok || len(parts) != 1 {
			return false
		}
		hash := openpgpHashFunc(hashAlgo)
		if hash == 0 {
			return false
		}
		signature := make([]byte, (public.N.BitLen()+7)/8)
		if len(parts[0]) > len(signature) {
			return false
		}
		copy(signature[len(signature)-len(parts[0]):], parts[0])
		return rsa.VerifyPKCS1v15(public, hash, digest, signature) == nil
	}
	return false
}
