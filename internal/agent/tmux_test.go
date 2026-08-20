package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionName(t *testing.T) {
	if got, want := SessionName("3f2a-abc"), "tend-3f2a-abc"; got != want {
		t.Errorf("SessionName = %q, want %q", got, want)
	}
}

func TestWrapTmux(t *testing.T) {
	inner := LaunchCmd("/tmp/work", "abc-123", "fix the bug", "")
	c := WrapTmux(inner, "tend-abc-123", "/cfg/tmux.conf")

	want := []string{
		tmuxBinary, "-L", SocketName, "-f", "/cfg/tmux.conf",
		"new-session", "-s", "tend-abc-123", "-c", "/tmp/work", "--",
		binary, "--session-id", "abc-123", "-n", "fix the bug",
	}
	if !equalArgs(c.Args, want) {
		t.Errorf("Args =\n %v\nwant\n %v", c.Args, want)
	}
	if c.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", c.Dir)
	}
}

// A label with spaces must survive as one argv element. Passing the
// inner command after `--` is what guarantees that; a shell-string form
// would split it and claude would see a truncated -n value.
func TestWrapTmuxKeepsArgumentBoundaries(t *testing.T) {
	inner := LaunchCmd("/tmp/work", "abc-123", "fix the flaky retry test", "")
	c := WrapTmux(inner, "tend-abc-123", "/cfg/tmux.conf")

	var found bool
	for i, a := range c.Args {
		if a == "-n" && i+1 < len(c.Args) {
			if c.Args[i+1] != "fix the flaky retry test" {
				t.Fatalf("label argv element = %q, want it intact", c.Args[i+1])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no -n argument survived the wrap")
	}
}

func TestWrapTmuxWithoutDirOmitsC(t *testing.T) {
	inner := LaunchCmd("", "abc-123", "label", "")
	c := WrapTmux(inner, "tend-abc-123", "/cfg/tmux.conf")
	for _, a := range c.Args {
		if a == "-c" {
			t.Fatal("-c passed with no working directory")
		}
	}
}

func TestAttachCmd(t *testing.T) {
	c := AttachCmd("tend-abc-123", "/cfg/tmux.conf")
	want := []string{
		tmuxBinary, "-L", SocketName, "-f", "/cfg/tmux.conf",
		"attach-session", "-d", "-t", "tend-abc-123",
	}
	if !equalArgs(c.Args, want) {
		t.Errorf("Args = %v, want %v", c.Args, want)
	}
}

func TestHasSessionEmptyName(t *testing.T) {
	if HasSession("", "/cfg/tmux.conf") {
		t.Error("HasSession(\"\") = true, want false — an unnamed session is never live")
	}
}

func TestConfigPathRespectsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if want := filepath.Join("/xdg", "tend", "tmux.conf"); got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
}

func TestWriteConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path, err := WriteConfig()
	if err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written config: %v", err)
	}
	got := string(b)

	// The two lines whose absence would be a silent, confusing bug
	// rather than an obvious one: without unbind, a stray prefix key
	// spawns an invisible window; without escape-time, Claude Code's
	// esc-to-interrupt feels broken.
	for _, want := range []string{"unbind -a -T prefix", "escape-time 10", "bind -n C-h detach-client"} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q", want)
		}
	}

	// Rewriting is not an error — WriteConfig runs on every launch.
	if _, err := WriteConfig(); err != nil {
		t.Fatalf("second WriteConfig: %v", err)
	}
}
