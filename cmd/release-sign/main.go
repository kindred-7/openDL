// Command release-sign creates Ed25519 keys or signs an exact release manifest.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openDL/appupdate"
)

type assetArgs []string

func (a *assetArgs) String() string { return strings.Join(*a, ", ") }
func (a *assetArgs) Set(value string) error {
	*a = append(*a, value)
	return nil
}

func main() {
	generate := flag.String("generate", "", "create a new signing-key directory")
	keyPath := flag.String("key", "", "private.key path")
	manifestPath := flag.String("manifest", "", "UTF-8 manifest.json path")
	outPath := flag.String("out", "releases.json", "signed envelope output")
	verifyPath := flag.String("verify", "", "verify a signed envelope instead of signing")
	publicPath := flag.String("public", "", "public.key path for -verify")
	var assets assetArgs
	flag.Var(&assets, "asset", "local artifact as os/arch[/kind]=path; repeat for manifest assets")
	flag.Parse()
	var err error
	if *verifyPath != "" {
		err = verifyEnvelope(*verifyPath, *publicPath, assets)
	} else {
		err = runWithAssets(*generate, *keyPath, *manifestPath, *outPath, assets)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

// verifyEnvelope 校验已签名的发布信封；可逐个复核本地安装包的大小与摘要。
func verifyEnvelope(envelopePath, publicPath string, assets []string) error {
	if envelopePath == "" {
		return fmt.Errorf("-verify 需要已签名信封路径")
	}
	if publicPath == "" {
		return fmt.Errorf("-verify 需要 -public 指定公钥")
	}
	envelopeData, err := os.ReadFile(envelopePath)
	if err != nil {
		return err
	}
	var envelope appupdate.Envelope
	if err := json.Unmarshal(envelopeData, &envelope); err != nil {
		return fmt.Errorf("信封格式无效: %w", err)
	}
	publicKey, err := os.ReadFile(publicPath)
	if err != nil {
		return err
	}
	now := time.Now()
	manifest, err := appupdate.DecodeManifest(appupdate.Config{PublicKey: string(publicKey)}, envelope, now)
	if err != nil {
		return err
	}
	if err := appupdate.ValidateManifestForSigning(*manifest, now); err != nil {
		return err
	}
	if err := checkLocalAssets(manifest.Assets, assets, false); err != nil {
		return err
	}
	fmt.Printf("验证通过: version %s，%d 个平台包\n", manifest.Version, len(manifest.Assets))
	return nil
}

func run(generate, keyPath, manifestPath, outPath string) error {
	return runWithAssets(generate, keyPath, manifestPath, outPath, nil)
}

func runWithAssets(generate, keyPath, manifestPath, outPath string, assets []string) error {
	if generate != "" {
		if len(assets) != 0 {
			return fmt.Errorf("-asset cannot be used with -generate")
		}
		if err := os.Mkdir(generate, 0700); err != nil {
			return err
		}
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		if err = writeNew(filepath.Join(generate, "private.key"), []byte(base64.StdEncoding.EncodeToString(private)+"\n"), 0600); err != nil {
			return err
		}
		if err = writeNew(filepath.Join(generate, "public.key"), []byte(base64.StdEncoding.EncodeToString(public)+"\n"), 0644); err != nil {
			return err
		}
		fmt.Println("Created signing keys in", generate)
		return nil
	}
	if keyPath == "" || manifestPath == "" {
		return fmt.Errorf("use -generate DIR or -key FILE -manifest FILE -out FILE")
	}
	encoded, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(key) != ed25519.PrivateKeySize || !bytes.Equal(key, ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])) {
		return fmt.Errorf("invalid Ed25519 private key")
	}
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if len(payload) > 512<<10 {
		return fmt.Errorf("manifest too large")
	}
	var manifest appupdate.Manifest
	if err = json.Unmarshal(payload, &manifest); err != nil {
		return err
	}
	if err = appupdate.ValidateManifestForSigning(manifest, time.Now()); err != nil {
		return err
	}
	if len(assets) > 0 {
		if err = verifyLocalAssets(manifest.Assets, assets); err != nil {
			return err
		}
	}
	envelope := appupdate.Envelope{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), payload))}
	output, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(outPath, append(output, '\n'), 0644)
}

func verifyLocalAssets(assets []appupdate.Asset, args []string) error {
	return checkLocalAssets(assets, args, true)
}

// A bare platform remains valid when it identifies one artifact. The kind is
// required when a platform provides both a binary and an installer.
func checkLocalAssets(assets []appupdate.Asset, args []string, requireAll bool) error {
	matched := make(map[int]bool, len(args))
	for _, arg := range args {
		platform, path, ok := strings.Cut(arg, "=")
		parts := strings.Split(platform, "/")
		if !ok || path == "" || (len(parts) != 2 && len(parts) != 3) || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] != "binary" && parts[2] != "installer") {
			return fmt.Errorf("invalid -asset mapping %q; use os/arch[/binary|installer]=path", platform)
		}
		index := -1
		for i, asset := range assets {
			kind, err := appupdate.AssetKind(asset)
			if err != nil {
				return err
			}
			if asset.OS != parts[0] || asset.Arch != parts[1] || (len(parts) == 3 && kind != parts[2]) {
				continue
			}
			if index >= 0 {
				return fmt.Errorf("ambiguous -asset mapping %q; specify the package kind", platform)
			}
			index = i
		}
		if index < 0 || matched[index] {
			return fmt.Errorf("unknown or duplicate -asset mapping %q", platform)
		}
		if err := verifyLocalAsset(path, assets[index]); err != nil {
			return fmt.Errorf("%s: %w", platform, err)
		}
		matched[index] = true
		fmt.Printf("Verified artifact: %s %s\n", platform, path)
	}
	if requireAll && len(matched) != len(assets) {
		return fmt.Errorf("provide exactly one -asset mapping for each manifest artifact")
	}
	return nil
}

func verifyLocalAsset(path string, asset appupdate.Asset) error {
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("artifact is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, opened) {
		return fmt.Errorf("artifact changed while opening")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(f, appupdate.MaxArtifactSize+1))
	if err != nil {
		return err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !after.Mode().IsRegular() || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("artifact changed while reading")
	}
	if size != asset.Size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), asset.SHA256) {
		return fmt.Errorf("artifact size or SHA-256 does not match the manifest")
	}
	return nil
}
