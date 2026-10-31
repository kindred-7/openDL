// Package installerconfig transfers one-time installer choices to the app.
package installerconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func markerPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opendl", "installer-autostart.json"), nil
}

// WriteAutoStart records a choice without overwriting the user's settings.
func WriteAutoStart(enabled bool, executable string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("installer executable path must be absolute")
	}
	path, err := markerPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Enabled    bool   `json:"enabled"`
		Executable string `json:"executable"`
	}{enabled, filepath.Clean(executable)})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".installer-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ReadAutoStart leaves the marker in place until the app has persisted it.
func ReadAutoStart(executable string) (bool, bool, error) {
	path, err := markerPath()
	if err != nil {
		return false, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var value struct {
		Enabled    *bool  `json:"enabled"`
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return false, false, err
	}
	if value.Enabled == nil || !filepath.IsAbs(value.Executable) {
		return false, false, errors.New("missing installer autostart choice")
	}
	if !strings.EqualFold(filepath.Clean(value.Executable), filepath.Clean(executable)) {
		return false, false, nil
	}
	return *value.Enabled, true, nil
}

func ClearAutoStart() error {
	path, err := markerPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
