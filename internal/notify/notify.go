// Package notify posts a desktop notification. It is an I/O edge like
// jira: stdlib only, and every caller is expected to treat a failure as
// something to report, not to act on.
package notify

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"
)

// ErrUnsupported is returned off macOS, where there is nothing to send with.
var ErrUnsupported = errors.New("notifications are only supported on macOS")

// sendTimeout bounds one send so a wedged helper cannot hold a tool call.
const sendTimeout = 5 * time.Second

// Seams for tests: nothing here executes a real binary under `go test`.
var (
	lookPath = exec.LookPath
	goos     = runtime.GOOS
	run      = func(c *exec.Cmd) error { return c.Run() }
)

// DefaultActivate is the bundle id a click brings forward when the message
// has no URL and no terminal is configured: the user's terminal, Ghostty.
const DefaultActivate = "com.mitchellh.ghostty"

// Message is one notification. URL and Activate are optional and only
// honoured by terminal-notifier: a click opens URL when it is set, and
// otherwise activates the app with bundle id Activate (DefaultActivate
// when empty).
type Message struct {
	Title, Body, URL, Activate string
}

// Send posts a notification.
func Send(ctx context.Context, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	cmd, err := command(ctx, m)
	if err != nil {
		return err
	}
	return run(cmd)
}

// command builds the process that posts the notification. Title and body
// travel as argv, never as part of the AppleScript source, so quotes or
// backslashes in them cannot break out of it.
func command(ctx context.Context, m Message) (*exec.Cmd, error) {
	if goos != "darwin" {
		return nil, ErrUnsupported
	}
	if _, err := lookPath("terminal-notifier"); err == nil {
		args := []string{"-title", m.Title, "-message", m.Body, "-group", "tend"}
		if m.URL != "" {
			args = append(args, "-open", m.URL)
		} else {
			activate := m.Activate
			if activate == "" {
				activate = DefaultActivate
			}
			args = append(args, "-activate", activate)
		}
		return exec.CommandContext(ctx, "terminal-notifier", args...), nil
	}
	// osascript's display notification cannot set a click action, so the
	// URL and the activate id are ignored here.
	return exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		m.Title, m.Body), nil
}
