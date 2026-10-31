package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOnlyRecognizesOpenDLInstaller(t *testing.T) {
	if !isOpenDLInstaller("Name \"OpenDL\"\nWriteRegStr HKCU \"Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\com.example.opendl\"") {
		t.Fatal("OpenDL script not recognized")
	}
	for _, script := range []string{"Name \"Other\"\nUninstall\\com.example.opendl", "Name \"OpenDL\"\nUninstall\\different"} {
		if isOpenDLInstaller(script) {
			t.Fatalf("recognized unrelated installer: %q", script)
		}
	}
}

func TestProjectRootStopsAtOtherProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "package-windows-installer.ps1"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mygo.json"), []byte(`{"name":"OpenDL","identifier":"com.example.opendl","version":"1.2.3"}`), 0600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "build", ".staging-windows-amd64-test")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	got, c := projectRootFrom(child)
	if got != root || c.Version != "1.2.3" {
		t.Fatalf("got %q %+v", got, c)
	}
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "mygo.json"), []byte(`{"name":"Other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, _ := projectRootFrom(other); got != "" {
		t.Fatalf("crossed unrelated project: %s", got)
	}
}
