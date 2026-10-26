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

// Send posts a notification with the given title and body.
func Send(ctx context.Context, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	cmd, err := command(ctx, title, body)
	if err != nil {
		return err
	}
	return run(cmd)
}

// command builds the process that posts the notification. Title and body
// travel as argv, never as part of the AppleScript source, so quotes or
// backslashes in them cannot break out of it.
func command(ctx context.Context, title, body string) (*exec.Cmd, error) {
	if goos != "darwin" {
		return nil, ErrUnsupported
	}
	if _, err := lookPath("terminal-notifier"); err == nil {
		return exec.CommandContext(ctx, "terminal-notifier",
			"-title", title, "-message", body, "-group", "tend"), nil
	}
	return exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body), nil
}
