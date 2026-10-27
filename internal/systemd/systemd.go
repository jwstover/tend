// Package systemd installs the per-user systemd timer that runs
// `tend schedule tick` every minute on Linux (tend task #653). It is the
// sibling of internal/launchd: an I/O edge, the only package that writes
// systemd user units or runs systemctl. Stdlib only.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Unit is the stem of the service and timer unit file names.
const Unit = "tend-schedule-tick"

// Config is everything the service unit bakes in. A systemd user service
// starts with no TEND_DB and a bare PATH, so each is spelled out.
type Config struct {
	Executable string // absolute path of the tend binary
	DBPath     string // absolute database path, passed as --db
	Path       string // PATH for the tick, so tmux and claude are found
	LogPath    string // stdout and stderr of every tick, appended
}

// runCmd is the seam tests replace so systemctl is never run.
var runCmd = func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() }

// quote renders one ExecStart word with systemd's quoting rules.
func quote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return strings.ReplaceAll(s, "%", "%%")
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	return `"` + r.Replace(s) + `"`
}

// ServiceUnit renders the oneshot service that performs one tick.
func ServiceUnit(c Config) []byte {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=tend schedule tick\n\n[Service]\nType=oneshot\n")
	args := []string{c.Executable, "--db", c.DBPath, "schedule", "tick"}
	for i, a := range args {
		args[i] = quote(a)
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	fmt.Fprintf(&b, "Environment=%s\n", quote("PATH="+c.Path))
	fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=append:%s\n", c.LogPath, c.LogPath)
	return []byte(b.String())
}

// TimerUnit renders the timer that fires the service every minute.
func TimerUnit() []byte {
	return []byte("[Unit]\nDescription=run tend schedule tick every minute\n\n" +
		"[Timer]\nOnCalendar=minutely\nAccuracySec=10s\nPersistent=false\n\n" +
		"[Install]\nWantedBy=timers.target\n")
}

// UnitDir is ${XDG_CONFIG_HOME:-~/.config}/systemd/user.
func UnitDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return runCmd(exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...))
}

// Install writes both units and enables the timer, returning the unit
// directory. enable --now on an enabled timer is a no-op restart, so a
// reinstall is idempotent.
func Install(ctx context.Context, c Config) (string, error) {
	dir, err := UnitDir()
	if err != nil {
		return "", err
	}
	for _, d := range []string{dir, filepath.Dir(c.LogPath)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", d, err)
		}
	}
	files := map[string][]byte{Unit + ".service": ServiceUnit(c), Unit + ".timer": TimerUnit()}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return "", fmt.Errorf("writing %s: %w", filepath.Join(dir, name), err)
		}
	}
	if out, err := systemctl(ctx, "daemon-reload"); err != nil {
		return "", fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := systemctl(ctx, "enable", "--now", Unit+".timer"); err != nil {
		return "", fmt.Errorf("systemctl enable: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

// Uninstall disables the timer and removes both units; each is fine to be
// missing already.
func Uninstall(ctx context.Context) error {
	dir, err := UnitDir()
	if err != nil {
		return err
	}
	// Not loaded is the common case: nothing to undo.
	_, _ = systemctl(ctx, "disable", "--now", Unit+".timer")
	for _, name := range []string{Unit + ".timer", Unit + ".service"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", filepath.Join(dir, name), err)
		}
	}
	_, _ = systemctl(ctx, "daemon-reload")
	return nil
}
