//go:build windows

package main

import (
	"strings"
	"testing"

	"openDL/download"
)

func TestProxyPasswordWindowsProtectionAndEndpointBinding(t *testing.T) {
	config := download.ProxyConfig{Mode: "manual", Address: "http://proxy.test:8080", Username: "name", Password: "private-password-测试"}
	protected, err := sealProxyPassword(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(protected, config.Password) {
		t.Fatal("password appears in protected data")
	}
	password, err := openProxyPassword(config, protected)
	if err != nil || password != config.Password {
		t.Fatalf("password could not be restored: %v", err)
	}
	config.Address = "http://another-proxy.test:8080"
	if _, err := openProxyPassword(config, protected); err == nil {
		t.Fatal("saved password reused for a different proxy")
	}
}
