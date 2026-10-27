package launchd

import (
	"context"
	"encoding/xml"
	"os"
	"os/exec"
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

func TestPlistContentAndEscaping(t *testing.T) {
	out := string(Plist(Config{
		Executable: "/opt/a & b/tend",
		DBPath:     "/data/<x>/tend.db",
		Path:       "/usr/bin:/opt/homebrew/bin",
		LogPath:    "/data/schedule.log",
	}))
	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/opt/a &amp; b/tend</string>",
		"<string>--db</string>",
		"<string>/data/&lt;x&gt;/tend.db</string>",
		"<string>schedule</string>",
		"<string>tick</string>",
		"<integer>60</integer>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<string>Background</string>",
		"<string>/usr/bin:/opt/homebrew/bin</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "/data/schedule.log") != 2 {
		t.Errorf("log path should appear for stdout and stderr:\n%s", out)
	}
	d := xml.NewDecoder(strings.NewReader(out))
	d.Strict = false
	for {
		if _, err := d.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("plist is not well-formed XML: %v", err)
			}
			break
		}
	}
}

func TestInstallUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	calls := stubRun(t)
	ctx := context.Background()
	c := Config{Executable: "/bin/tend", DBPath: "/d/tend.db", Path: "/bin", LogPath: home + "/logs/schedule.log"}

	for i := 0; i < 2; i++ { // twice is fine
		*calls = nil
		path, err := Install(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if want := home + "/Library/LaunchAgents/" + Label + ".plist"; path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 2 ||
			!strings.Contains((*calls)[0], "bootout gui/") || !strings.HasSuffix((*calls)[0], "/"+Label) ||
			!strings.Contains((*calls)[1], "bootstrap gui/") || !strings.HasSuffix((*calls)[1], path) {
			t.Fatalf("calls = %q", *calls)
		}
	}

	*calls = nil
	if err := Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], "bootout") {
		t.Fatalf("calls = %q", *calls)
	}
	if _, err := os.Stat(home + "/Library/LaunchAgents/" + Label + ".plist"); !os.IsNotExist(err) {
		t.Fatalf("plist still present: %v", err)
	}
	if err := Uninstall(ctx); err != nil { // nothing installed
		t.Fatal(err)
	}
}
