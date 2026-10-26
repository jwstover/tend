package notify

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func stub(t *testing.T, os string, haveNotifier bool) {
	t.Helper()
	oldOS, oldLook, oldRun := goos, lookPath, run
	t.Cleanup(func() { goos, lookPath, run = oldOS, oldLook, oldRun })
	goos = os
	lookPath = func(string) (string, error) {
		if haveNotifier {
			return "/bin/terminal-notifier", nil
		}
		return "", exec.ErrNotFound
	}
}

func TestCommandArgv(t *testing.T) {
	title, body := `a "quoted" \ title`, "line1\nline2"
	tests := []struct {
		name         string
		haveNotifier bool
		want         []string
	}{
		{"terminal-notifier", true, []string{"terminal-notifier", "-title", title, "-message", body, "-group", "tend"}},
		{"osascript", false, []string{"osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run", title, body}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub(t, "darwin", tt.haveNotifier)
			cmd, err := command(context.Background(), title, body)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd.Args, tt.want) {
				t.Errorf("args = %q, want %q", cmd.Args, tt.want)
			}
		})
	}
}

func TestSendUnsupportedOffDarwin(t *testing.T) {
	stub(t, "linux", true)
	run = func(*exec.Cmd) error { t.Error("ran a command off darwin"); return nil }
	if err := Send(context.Background(), "t", "b"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestSendRunsCommandAndReturnsItsError(t *testing.T) {
	stub(t, "darwin", false)
	boom := errors.New("boom")
	var got *exec.Cmd
	run = func(c *exec.Cmd) error { got = c; return boom }
	if err := Send(context.Background(), "t", "b"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	if got == nil || got.Args[0] != "osascript" {
		t.Errorf("ran %v", got)
	}
}
