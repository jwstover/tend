package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionName(t *testing.T) {
	if got, want := SessionName("3f2a-abc"), "tend-3f2a-abc"; got != want {
		t.Errorf("SessionName = %q, want %q", got, want)
	}
}

func TestWrapTmux(t *testing.T) {
	inner := LaunchCmd("/tmp/work", "abc-123", "fix the bug", "", "")
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
	inner := LaunchCmd("/tmp/work", "abc-123", "fix the flaky retry test", "", "")
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
	inner := LaunchCmd("", "abc-123", "label", "", "")
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

// CapturePane runs tmux directly rather than returning an *exec.Cmd, so
// there's no Args slice to inspect the way TestWrapTmux/TestAttachCmd do
// — same constraint HasSession is already under. This exercises the
// degrade-quietly contract instead: a session that isn't there returns
// "" and no error, never a panic or a surfaced failure.
func TestCapturePaneMissingSessionDegradesQuietly(t *testing.T) {
	if !TmuxInstalled() {
		t.Skip("tmux not on PATH")
	}
	got, err := CapturePane("tend-does-not-exist", "/cfg/tmux.conf")
	if err != nil {
		t.Fatalf("CapturePane: %v, want a swallowed error", err)
	}
	if got != "" {
		t.Errorf("CapturePane(missing session) = %q, want empty", got)
	}
}

// TestCapturePaneLiveSession runs CapturePane against a real, live tmux
// session on tend's own socket — the gap that let a real bug reach a
// running tend process undetected: CapturePane originally targeted -t
// with the same "=exact-name" form HasSession/KillSession use for a
// target-session, but capture-pane's -t is a target-pane, where tmux
// rejects "=name" with "can't find pane" even though the named session
// is unquestionably alive. TestCapturePaneMissingSessionDegradesQuietly
// only ever exercised a session that was never there, so it degraded to
// "", nil either way and could not tell a real failure from the correct
// missing-session behavior. This test would have failed against that
// bug: it asserts the captured text actually contains what the live
// session printed, not just that CapturePane returned without error.
func TestCapturePaneLiveSession(t *testing.T) {
	if !TmuxInstalled() {
		t.Skip("tmux not on PATH")
	}
	cfgPath, err := WriteConfig()
	if err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	name := "tend-capturepane-live-test"
	kill := exec.Command(tmuxBinary, "-L", SocketName, "-f", cfgPath, "kill-session", "-t", "="+name)
	_ = kill.Run() // best-effort: clear a session left behind by a prior failed run

	const marker = "CAPTURE_PANE_LIVE_TEST_MARKER"
	newSession := exec.Command(tmuxBinary, "-L", SocketName, "-f", cfgPath,
		"new-session", "-d", "-s", name, "--", "sh", "-c", "printf '"+marker+"'; sleep 5")
	if err := newSession.Run(); err != nil {
		t.Fatalf("starting live test session: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmuxBinary, "-L", SocketName, "-f", cfgPath, "kill-session", "-t", "="+name).Run()
	})

	// new-session returns once tmux has created the session, not once the
	// pane's child process has actually run — poll briefly rather than
	// racing a single capture against sh's own startup time.
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for {
		got, err = CapturePane(name, cfgPath)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		if strings.Contains(got, marker) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(got, marker) {
		t.Errorf("CapturePane(live session) = %q, want it to contain %q", got, marker)
	}
}

func TestCapturePaneEmptyName(t *testing.T) {
	got, err := CapturePane("", "/cfg/tmux.conf")
	if err != nil || got != "" {
		t.Errorf("CapturePane(\"\") = (%q, %v), want (\"\", nil) — an unnamed session is never live", got, err)
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
