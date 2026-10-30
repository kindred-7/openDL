package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openDL/download"
)

func TestProxySettingsDoNotSerializeRuntimePassword(t *testing.T) {
	value := defaultAppPreferences()
	value.Proxy = download.ProxyConfig{Mode: "manual", Address: "http://proxy.test:8080", Username: "name", Password: "runtime-secret"}
	value.ProxySecret = "opaque-protected-value"
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := writeAppPreferencesTo(path, value); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("runtime-secret")) || !bytes.Contains(data, []byte("opaque-protected-value")) {
		t.Fatalf("credential persistence: %s err=%v", data, err)
	}
	var restored appPreferences
	if err := json.Unmarshal(data, &restored); err != nil || restored.Proxy.Password != "" || restored.Proxy.Mode != "manual" {
		t.Fatalf("settings restore: %+v err=%v", restored.Proxy, err)
	}
	value.Proxy.Mode = "direct"
	if err := writeAppPreferencesTo(path, value); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &restored) != nil || restored.Proxy.Mode != "direct" {
		t.Fatalf("atomic settings replacement: %v", err)
	}
}

func TestUnreadableSavedProxyBlocksNetworkUntilReconfigured(t *testing.T) {
	value := defaultAppPreferences()
	value.Proxy = download.ProxyConfig{Mode: "manual", Address: "http://proxy.test:8080", Username: "name"}
	value.ProxySecret = "invalid-data"
	manager := download.NewManager(t.TempDir(), 1, 1)
	if _, err := initializeDownloadProxy(manager, value); err == nil {
		t.Fatal("invalid saved password was accepted")
	}
	changes := make(chan struct{}, 1)
	manager.OnChange(func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	})
	id := manager.Add(download.Request{URL: "https://download.example.test/file"})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	failed := false
	for !failed {
		for _, snapshot := range manager.Snapshots() {
			if snapshot.ID == id && snapshot.State == download.ErrorS {
				if !strings.Contains(snapshot.Err, "代理密码") {
					t.Fatalf("unexpected task failure: %s", snapshot.Err)
				}
				failed = true
			}
		}
		if failed {
			break
		}
		select {
		case <-changes:
		case <-deadline.C:
			t.Fatal("unavailable credentials did not stop the task")
		}
	}
	if err := manager.SetProxyConfig(download.ProxyConfig{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("recovered")) }))
	defer srv.Close()
	snapshot := waitDone(t, manager, manager.Add(download.Request{URL: srv.URL + "/recovered.bin"}))
	data, err := os.ReadFile(snapshot.FilePath)
	if err != nil || string(data) != "recovered" {
		t.Fatalf("network did not recover after explicit reconfiguration: %v", err)
	}
}
