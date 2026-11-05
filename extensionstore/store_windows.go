package extensionstore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// DefaultURL returns the store listing for the browser that handles HTTPS links.
func DefaultURL() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`, registry.QUERY_VALUE)
	if err != nil {
		return chromeURL
	}
	defer key.Close()
	progID, _, err := key.GetStringValue("ProgId")
	if err == nil && strings.HasPrefix(strings.ToLower(progID), "firefoxurl") {
		return Firefox.storeURL()
	}
	return chromeURL
}

// Open starts the selected browser with its extension listing.
func Open(browser Browser) error {
	var relativePath string
	switch browser {
	case Chrome:
		relativePath = `Google\Chrome\Application\chrome.exe`
	case Edge:
		relativePath = `Microsoft\Edge\Application\msedge.exe`
	case Firefox:
		relativePath = `Mozilla Firefox\firefox.exe`
	default:
		return fmt.Errorf("unsupported browser %q", browser)
	}
	path := findBrowser(relativePath)
	if path == "" {
		return ErrBrowserNotFound
	}
	cmd := exec.Command(path, browser.storeURL())
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func findBrowser(relativePath string) string {
	name := filepath.Base(relativePath)
	appPath := `Software\Microsoft\Windows\CurrentVersion\App Paths\` + name
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			key, err := registry.OpenKey(root, appPath, registry.QUERY_VALUE|view)
			if err != nil {
				continue
			}
			path, valueType, err := key.GetStringValue("")
			key.Close()
			if err != nil {
				continue
			}
			if valueType == registry.EXPAND_SZ {
				path, err = registry.ExpandString(path)
				if err != nil {
					continue
				}
			}
			path = strings.Trim(strings.TrimSpace(path), `"`)
			if strings.EqualFold(filepath.Base(path), name) && isBrowserExecutable(path) {
				return path
			}
		}
	}
	for _, env := range []string{"LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)"} {
		path := filepath.Join(os.Getenv(env), relativePath)
		if isBrowserExecutable(path) {
			return path
		}
	}
	return ""
}

func isBrowserExecutable(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
