package systemd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func stubRun(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	old := runCmd
	runCmd = func(c *exec.Cmd) ([]byte, error) {
		calls = append(calls, strings.Join(c.Args, " "))
		return nil, nil
	}
	t.Cleanup(func() { runCmd = old })
	return &calls
}

func TestServiceUnit(t *testing.T) {
	out := string(ServiceUnit(Config{
		Executable: "/opt/my tend/tend",
		DBPath:     "/data/tend.db",
		Path:       "/usr/bin:/opt/homebrew/bin",
		LogPath:    "/data/schedule.log",
	}))
	for _, want := range []string{
		"Description=tend schedule tick",
		"Type=oneshot",
		`ExecStart="/opt/my tend/tend" --db /data/tend.db schedule tick`,
		"Environment=PATH=/usr/bin:/opt/homebrew/bin",
		"StandardOutput=append:/data/schedule.log",
		"StandardError=append:/data/schedule.log",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("service missing %q:\n%s", want, out)
		}
	}
}

func TestTimerUnit(t *testing.T) {
	out := string(TimerUnit())
	for _, want := range []string{"OnCalendar=minutely", "AccuracySec=10s", "Persistent=false", "WantedBy=timers.target"} {
		if !strings.Contains(out, want) {
			t.Errorf("timer missing %q:\n%s", want, out)
		}
	}
}

func TestUnitDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	if d, _ := UnitDir(); d != "/x/cfg/systemd/user" {
		t.Errorf("dir = %q", d)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h")
	if d, _ := UnitDir(); d != "/h/.config/systemd/user" {
		t.Errorf("dir = %q", d)
	}
}

func TestInstallUninstall(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	calls := stubRun(t)
	ctx := context.Background()
	c := Config{Executable: "/bin/tend", DBPath: "/d/tend.db", Path: "/bin", LogPath: filepath.Join(cfg, "logs", "schedule.log")}

	dir, err := Install(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cfg, "systemd", "user"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	for _, n := range []string{Unit + ".service", Unit + ".timer"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + Unit + ".timer",
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %q, want %q", *calls, want)
	}

	*calls = nil
	for i := 0; i < 2; i++ { // second run: nothing installed
		if err := Uninstall(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{Unit + ".service", Unit + ".timer"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Fatalf("%s still present: %v", n, err)
		}
	}
	if (*calls)[0] != "systemctl --user disable --now "+Unit+".timer" || (*calls)[1] != "systemctl --user daemon-reload" {
		t.Fatalf("calls = %q", *calls)
	}
}
