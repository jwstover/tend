// Package launchd installs the per-user launchd agent that runs
// `tend schedule tick` every minute (tend task #645). It is an I/O edge like
// internal/jira: the only package that writes a LaunchAgents plist or runs
// launchctl. Stdlib only.
package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Label is the launchd job label, and the plist's file name stem.
const Label = "com.github.jwstover.tend.schedule-tick"

// Config is everything the plist bakes in. launchd starts jobs with no
// TEND_DB/XDG variables and a bare PATH, so each is spelled out.
type Config struct {
	Executable string // absolute path of the tend binary
	DBPath     string // absolute database path, passed as --db
	Path       string // PATH for the tick, so tmux and claude are found
	LogPath    string // stdout and stderr of every tick, appended
}

// runCmd is the seam tests replace so launchctl is never run.
var runCmd = func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() }

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Plist renders the launchd agent definition for c.
func Plist(c Config) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	str := func(k, v string) { fmt.Fprintf(&b, "\t<key>%s</key>\n\t<string>%s</string>\n", k, esc(v)) }
	str("Label", Label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range []string{c.Executable, "--db", c.DBPath, "schedule", "tick"} {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", esc(a))
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>StartInterval</key>\n\t<integer>60</integer>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	str("ProcessType", "Background")
	fmt.Fprintf(&b, "\t<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>PATH</key>\n\t\t<string>%s</string>\n\t</dict>\n", esc(c.Path))
	str("StandardOutPath", c.LogPath)
	str("StandardErrorPath", c.LogPath)
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// PlistPath is ~/Library/LaunchAgents/<Label>.plist.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func domainTarget() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func launchctl(ctx context.Context, args ...string) ([]byte, error) {
	return runCmd(exec.CommandContext(ctx, "launchctl", args...))
}

// Install writes the plist and (re)loads the agent, returning the plist
// path. Booting out first makes a reinstall idempotent.
func Install(ctx context.Context, c Config) (string, error) {
	path, err := PlistPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(c.LogPath), err)
	}
	if err := os.WriteFile(path, Plist(c), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	// Not loaded yet is the common case; a real failure surfaces at bootstrap.
	_, _ = launchctl(ctx, "bootout", domainTarget()+"/"+Label)
	if out, err := launchctl(ctx, "bootstrap", domainTarget(), path); err != nil {
		return "", fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return path, nil
}

// Uninstall boots the agent out and removes the plist; both are fine to be
// missing already.
func Uninstall(ctx context.Context) error {
	path, err := PlistPath()
	if err != nil {
		return err
	}
	// Exit 3 / "no such process" when not loaded: nothing to undo.
	_, _ = launchctl(ctx, "bootout", domainTarget()+"/"+Label)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}
