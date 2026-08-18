package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// withFakeTendOnPath points $PATH at a temp dir containing an executable
// named "tend" (or "tend.exe" on Windows), so WriteMCPConfig's
// exec.LookPath("tend") succeeds deterministically regardless of
// whether the real binary happens to be installed in the environment
// running the tests.
func withFakeTendOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "tend"
	if runtime.GOOS == "windows" {
		name = "tend.exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing fake tend: %v", err)
	}
	t.Setenv("PATH", dir)
	return path
}

func TestWriteMCPConfigWritesTendServerEntry(t *testing.T) {
	tendPath := withFakeTendOnPath(t)

	path, cleanup, err := WriteMCPConfig(42, "/data/tend.db")
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	defer cleanup()

	if path == "" {
		t.Fatal("path is empty, want a config file path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}
	var cfg mcpConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshaling config: %v", err)
	}
	entry, ok := cfg.MCPServers["tend"]
	if !ok {
		t.Fatal(`mcpServers["tend"] missing`)
	}
	if entry.Command != tendPath {
		t.Errorf("Command = %q, want %q", entry.Command, tendPath)
	}
	want := []string{"mcp", "--task-id", "42", "--db", "/data/tend.db"}
	if !equalArgs(entry.Args, want) {
		t.Errorf("Args = %v, want %v", entry.Args, want)
	}
}

func TestWriteMCPConfigCleanupRemovesFile(t *testing.T) {
	withFakeTendOnPath(t)

	path, cleanup, err := WriteMCPConfig(1, "/data/tend.db")
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file should exist before cleanup: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file should be removed after cleanup, stat err = %v", err)
	}
}

func TestWriteMCPConfigDegradesQuietlyWithoutTendOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	path, cleanup, err := WriteMCPConfig(1, "/data/tend.db")
	if err != nil {
		t.Fatalf("WriteMCPConfig should degrade quietly, got error: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty when tend isn't on $PATH", path)
	}
	cleanup() // must not panic
}
