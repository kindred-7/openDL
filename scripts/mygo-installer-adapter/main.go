// Command mygo-installer-adapter routes this project's MyGo installer build
// to its custom packer. Other NSIS calls pass through to the original tool.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var outFile = regexp.MustCompile(`(?m)^OutFile\s+"([^"\r\n]+)"\s*$`)

type projectConfig struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Version    string `json:"version"`
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	real := filepath.Join(filepath.Dir(self), "makensis-real.exe")
	if os.Getenv("OPENDL_SKIP_MYGO_INSTALLER") != "" {
		return passthrough(real, args)
	}
	var source string
	for _, arg := range args {
		if strings.EqualFold(filepath.Ext(arg), ".nsi") {
			source = arg
		}
	}
	if source == "" {
		return passthrough(real, args)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return passthrough(real, args)
	}
	script := strings.TrimPrefix(string(data), "\ufeff")
	if !isOpenDLInstaller(script) {
		return passthrough(real, args)
	}
	match := outFile.FindStringSubmatch(script)
	if len(match) != 2 {
		fmt.Fprintln(os.Stderr, "OpenDL installer: cannot find MyGo output path")
		return 1
	}
	output := strings.ReplaceAll(match[1], "$$", "$")
	stage := filepath.Dir(output)
	root, config := projectRoot()
	if root == "" {
		root, config = projectRootFrom(stage)
	}
	if root == "" {
		return passthrough(real, args)
	}
	arch := ""
	for _, candidate := range []string{"amd64", "arm64"} {
		if filepath.Base(output) == "OpenDL Setup "+config.Version+" "+candidate+".exe" {
			arch = candidate
			break
		}
	}
	if arch == "" {
		fmt.Fprintln(os.Stderr, "OpenDL installer: unsupported output name", output)
		return 1
	}
	if info, err := os.Stat(filepath.Join(stage, "OpenDL.exe")); err != nil || info.IsDir() {
		fmt.Fprintln(os.Stderr, "OpenDL installer: application executable missing in stage")
		return 1
	}
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File",
		filepath.Join(root, "scripts", "package-windows-installer.ps1"),
		"-Stage", stage, "-OutputFile", output, "-Version", config.Version, "-Architecture", arch)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "OpenDL installer packaging failed:", err)
		return 1
	}
	return 0
}

func isOpenDLInstaller(script string) bool {
	return strings.Contains(script, `Name "OpenDL"`) && strings.Contains(script, `Uninstall\com.example.opendl`)
}

func projectRoot() (string, projectConfig) {
	dir, err := os.Getwd()
	if err != nil {
		return "", projectConfig{}
	}
	return projectRootFrom(dir)
}

func projectRootFrom(dir string) (string, projectConfig) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", projectConfig{}
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "mygo.json"))
		if err == nil {
			var c projectConfig
			if json.Unmarshal(data, &c) == nil && c.Name == "OpenDL" && c.Identifier == "com.example.opendl" && c.Version != "" {
				if _, err := os.Stat(filepath.Join(dir, "scripts", "package-windows-installer.ps1")); err == nil {
					return dir, c
				}
			}
			// Do not walk past a different project into a parent project.
			return "", projectConfig{}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", projectConfig{}
		}
		dir = parent
	}
}

func passthrough(real string, args []string) int {
	cmd := exec.Command(real, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "cannot run original NSIS:", err)
		return 1
	}
	return 0
}
