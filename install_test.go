package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerRegistersTrailsWithoutMonitoring(t *testing.T) {
	home := t.TempDir()
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("install.sh", string(script))
	write("go", "#!/bin/sh\nexit 0\n")
	write("entire", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOME/registrations\"\n")
	write("launchctl", "#!/bin/sh\ntouch \"$HOME/monitoring-started\"\nexit 1\n")
	cmd := exec.Command("bash", filepath.Join(home, "install.sh"))
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+home+":/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	registrations, err := os.ReadFile(filepath.Join(home, "registrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"entire-tail", "entire-wtf", "entire-trails"} {
		path := filepath.Join(home, ".local/bin", name)
		if target, err := os.Readlink(path); err != nil || target != filepath.Join(home, "entire-tail") {
			t.Fatalf("%s: %s %v", name, target, err)
		}
		if !strings.Contains(string(registrations), "plugin install "+path+" --force") {
			t.Fatalf("missing registration: %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "monitoring-started")); !os.IsNotExist(err) {
		t.Fatal("installer started monitoring")
	}
}
