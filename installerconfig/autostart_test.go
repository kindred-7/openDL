package installerconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutoStartChoiceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "OpenDL.exe")
	if os.PathSeparator == '\\' {
		t.Setenv("APPDATA", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	for _, choice := range []bool{false, true, false} {
		if err := WriteAutoStart(choice, exe); err != nil {
			t.Fatal(err)
		}
		got, present, err := ReadAutoStart(exe)
		if err != nil || !present || got != choice {
			t.Fatalf("choice=%v got=%v present=%v err=%v", choice, got, present, err)
		}
		_, otherPresent, err := ReadAutoStart(filepath.Join(dir, "other", "OpenDL.exe"))
		if err != nil || otherPresent {
			t.Fatalf("choice leaked to another installation: %v %v", otherPresent, err)
		}
	}
	if err := ClearAutoStart(); err != nil {
		t.Fatal(err)
	}
	_, present, err := ReadAutoStart(exe)
	if err != nil || present {
		t.Fatalf("marker after clear: present=%v err=%v", present, err)
	}
}
