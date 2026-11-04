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
	if err := os.MkdirAll(filepath.Join(home, "docs/skills/thread-handover"), 0700); err != nil {
		t.Fatal(err)
	}
	write("docs/skills/thread-handover/SKILL.md", "skill body\n")
	if err := os.MkdirAll(filepath.Join(home, "docs/skills/threads-bootstrap"), 0700); err != nil {
		t.Fatal(err)
	}
	write("docs/skills/threads-bootstrap/SKILL.md", "bootstrap body\n")
	write("docs/skills/handover-command.md", "command body\n")
	write("docs/handover-sessions-skill.md", "---\nname: handover-sessions\n---\nbatch body\n")
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
	if b, err := os.ReadFile(filepath.Join(home, ".agents/skills/thread-handover/SKILL.md")); err != nil || string(b) != "skill body\n" {
		t.Fatalf("skill not installed: %q %v", b, err)
	}
	if target, err := os.Readlink(filepath.Join(home, ".claude/skills/thread-handover")); err != nil || target != filepath.Join(home, ".agents/skills/thread-handover") {
		t.Fatalf("skill link: %q %v", target, err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".agents/skills/threads-bootstrap/SKILL.md")); err != nil || string(b) != "bootstrap body\n" {
		t.Fatalf("bootstrap skill not installed: %q %v", b, err)
	}
	if target, err := os.Readlink(filepath.Join(home, ".claude/skills/threads-bootstrap")); err != nil || target != filepath.Join(home, ".agents/skills/threads-bootstrap") {
		t.Fatalf("bootstrap skill link: %q %v", target, err)
	}
	for rel, want := range map[string]string{
		".claude/commands/handover.md":              "command body\n",
		".claude/skills/handover-sessions/SKILL.md": "---\nname: handover-sessions\n---\nbatch body\n",
	} {
		if b, err := os.ReadFile(filepath.Join(home, rel)); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", rel, b, err)
		}
		if b, _ := os.ReadFile(filepath.Join(home, rel)); rel == ".claude/skills/handover-sessions/SKILL.md" && !strings.HasPrefix(string(b), "---\n") {
			t.Fatalf("installed skill must start with frontmatter: %q", b)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "monitoring-started")); !os.IsNotExist(err) {
		t.Fatal("installer started monitoring")
	}
}

func TestInstallerSkipsSymlinkedHandoverTargets(t *testing.T) {
	home := t.TempDir()
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("install.sh", string(script))
	write("docs/skills/thread-handover/SKILL.md", "skill\n")
	write("docs/skills/threads-bootstrap/SKILL.md", "bootstrap\n")
	write("docs/skills/handover-command.md", "new command\n")
	write("docs/handover-sessions-skill.md", "---\nnew batch\n")
	write("go", "#!/bin/sh\nexit 0\n")
	write("entire", "#!/bin/sh\nexit 0\n")
	write("launchctl", "#!/bin/sh\nexit 1\n")
	write("elsewhere/handover.md", "orig command\n")
	write("elsewhere/SKILL.md", "orig batch\n")
	cmdLink := filepath.Join(home, ".claude/commands/handover.md")
	skillLink := filepath.Join(home, ".claude/skills/handover-sessions/SKILL.md")
	for _, d := range []string{filepath.Dir(cmdLink), filepath.Dir(skillLink)} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere/handover.md"), cmdLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere/SKILL.md"), skillLink); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(home, "install.sh"))
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+home+":/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for f, want := range map[string]string{"elsewhere/handover.md": "orig command\n", "elsewhere/SKILL.md": "orig batch\n"} {
		if b, _ := os.ReadFile(filepath.Join(home, f)); string(b) != want {
			t.Fatalf("%s was written through: %q", f, b)
		}
	}
	for _, l := range []string{cmdLink, skillLink} {
		if fi, err := os.Lstat(l); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s no longer a symlink", l)
		}
	}
	want := "skipping ~/.claude/commands/handover.md: it is a symlink to " + filepath.Join(home, "elsewhere/handover.md") + "; update that file from docs/skills/handover-command.md"
	if !strings.Contains(string(out), want) {
		t.Fatalf("missing skip message: %s", out)
	}
}
