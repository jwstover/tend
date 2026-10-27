package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/launchd"
)

func runInstallCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "tend.db")
	cmd := newScheduleInstallCmd(func() string { return db })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestScheduleInstallPrint(t *testing.T) {
	old := goos
	goos = "darwin"
	t.Cleanup(func() { goos = old })
	oldInst := launchdInstall
	launchdInstall = func(context.Context, launchd.Config) (string, error) {
		t.Fatal("--print must not install")
		return "", nil
	}
	t.Cleanup(func() { launchdInstall = oldInst })
	t.Setenv("PATH", "/x/bin:/usr/bin")

	oldExe := executablePath
	exe := filepath.Join(t.TempDir(), "tend")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executablePath = oldExe })

	out, err := runInstallCmd(t, "--print")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>--db</string>", "<string>schedule</string>", "<string>tick</string>", "<integer>60</integer>", "/x/bin:/usr/bin"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestScheduleInstallRefusesBuildCache(t *testing.T) {
	old, oldExe := goos, executablePath
	goos = "darwin"
	executablePath = func() (string, error) { return os.Args[0], nil }
	t.Cleanup(func() { goos, executablePath = old, oldExe })
	dir := filepath.Join(t.TempDir(), "go-build123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "tend")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }
	if _, err := runInstallCmd(t, "--print"); err == nil || !strings.Contains(err.Error(), "make install") {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduleInstallNonDarwin(t *testing.T) {
	old := goos
	goos = "linux"
	t.Cleanup(func() { goos = old })
	if _, err := runInstallCmd(t); err == nil || !strings.Contains(err.Error(), "macOS-only") {
		t.Fatalf("install err = %v", err)
	}
	cmd := newScheduleUninstallCmd()
	cmd.SetArgs(nil)
	if err := cmd.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "macOS-only") {
		t.Fatalf("uninstall err = %v", err)
	}
}
