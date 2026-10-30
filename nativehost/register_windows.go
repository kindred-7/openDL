//go:build windows

package nativehost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const hostName = "com.example.opendl"
const chromeOrigin = "chrome-extension://gfimibmmfmmhhjfioegmhbkmkhoefleo/"
const chromeStoreOrigin = "chrome-extension://gppemfjpooijagmiigefeonekdkiodah/"

func manifestDir() string { return filepath.Join(os.Getenv("LOCALAPPDATA"), "opendl") }

// Browser native messaging passes the caller origin (Chromium) or manifest
// path (Firefox) as the first argument. Match only our own registrations.
func IsBrowserInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == chromeOrigin || args[0] == chromeStoreOrigin {
		return true
	}
	return strings.EqualFold(filepath.Clean(args[0]), filepath.Join(manifestDir(), hostName+"-firefox.json"))
}

// Register points each browser at the current executable. No compiler or
// separate helper binary is needed on an end user's machine.
func Register() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if os.Getenv("LOCALAPPDATA") == "" {
		return fmt.Errorf("LOCALAPPDATA is unavailable")
	}
	dir := manifestDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	type hostManifest struct {
		Name              string   `json:"name"`
		Description       string   `json:"description"`
		Path              string   `json:"path"`
		Type              string   `json:"type"`
		AllowedOrigins    []string `json:"allowed_origins,omitempty"`
		AllowedExtensions []string `json:"allowed_extensions,omitempty"`
	}
	base := hostManifest{Name: hostName, Description: "OpenDL native messaging host", Path: exe, Type: "stdio"}
	chrome := base
	chrome.AllowedOrigins = []string{chromeOrigin, chromeStoreOrigin}
	firefox := base
	firefox.AllowedExtensions = []string{"opendl@opendl.local"}
	for _, entry := range []struct {
		filename string
		manifest hostManifest
		browsers []string
	}{
		{hostName + ".json", chrome, []string{`Google\Chrome`, `Microsoft\Edge`}},
		{hostName + "-firefox.json", firefox, []string{`Mozilla`}},
	} {
		path := filepath.Join(dir, entry.filename)
		data, err := json.Marshal(entry.manifest)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return err
		}
		for _, browser := range entry.browsers {
			key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\`+browser+`\NativeMessagingHosts\`+hostName, registry.SET_VALUE)
			if err != nil {
				return err
			}
			err = key.SetStringValue("", path)
			key.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
