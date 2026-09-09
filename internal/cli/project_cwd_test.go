package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `tend projects cwd <name> <path>` sets the default, `cwd <name>` shows
// it, `--clear` removes it, and the listing carries it (tend task #190).
func TestProjectsCwdSetShowClear(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("app")

	out, err := runProjects(t, s, "cwd", "app", "/tmp/app/")
	if err != nil {
		t.Fatalf("projects cwd set: %v", err)
	}
	if !strings.Contains(out, "/tmp/app") {
		t.Errorf("set should echo the stored path: %q", out)
	}
	p, _ := s.ProjectByName(ctx, "app")
	if p.Cwd != "/tmp/app" {
		t.Errorf("Cwd = %q, want the cleaned /tmp/app", p.Cwd)
	}

	if out, err = runProjects(t, s, "cwd", "app"); err != nil {
		t.Fatalf("projects cwd show: %v", err)
	}
	if strings.TrimSpace(out) != "/tmp/app" {
		t.Errorf("show printed %q, want /tmp/app", out)
	}

	if out, err = runProjects(t, s); err != nil {
		t.Fatalf("projects: %v", err)
	}
	if !strings.Contains(out, "/tmp/app") {
		t.Errorf("listing should show the default cwd:\n%s", out)
	}

	if out, err = runProjects(t, s, "cwd", "app", "--clear"); err != nil {
		t.Fatalf("projects cwd --clear: %v", err)
	}
	if !strings.Contains(out, "cleared") {
		t.Errorf("--clear should say so: %q", out)
	}
	if p, _ = s.ProjectByName(ctx, "app"); p.Cwd != "" {
		t.Errorf("Cwd after --clear = %q, want unset", p.Cwd)
	}

	if out, err = runProjects(t, s, "cwd", "app"); err != nil {
		t.Fatalf("projects cwd show (unset): %v", err)
	}
	if !strings.Contains(out, "no default cwd") {
		t.Errorf("show on an unset default printed %q", out)
	}
}

// A relative path is resolved where the shell's cwd is known: stored as
// typed it would point somewhere different from every other directory.
func TestProjectsCwdResolvesRelativePaths(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("app")

	if _, err := runProjects(t, s, "cwd", "app", "./sub/../dir"); err != nil {
		t.Fatalf("projects cwd: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	p, _ := s.ProjectByName(ctx, "app")
	if want := filepath.Join(wd, "dir"); p.Cwd != want {
		t.Errorf("Cwd = %q, want the absolute %q", p.Cwd, want)
	}
}

// An unknown project fails with the same create-it hint the other
// subcommands give, and creates nothing.
func TestProjectsCwdUnknownProjectErrors(t *testing.T) {
	s := newFakeStore("app")
	_, err := runProjects(t, s, "cwd", "nope", "/tmp/x")
	if err == nil {
		t.Fatal("cwd on an unknown project should fail")
	}
	if !strings.Contains(err.Error(), "tend projects add") {
		t.Errorf("error should hint at creating it: %v", err)
	}
	if len(s.projects) != 2 {
		t.Errorf("a failed lookup created a project: %+v", s.projects)
	}
}
