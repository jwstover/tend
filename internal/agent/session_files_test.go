package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The files land in the given dir as absolute paths, wired to the given
// tend binary and database, and a brief-less session gets no prompt file.
func TestWriteSessionFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s1")
	files, err := WriteSessionFiles(dir, "/bin/tend", 42, "/data/tend.db", "the brief")
	if err != nil {
		t.Fatalf("WriteSessionFiles: %v", err)
	}
	read := func(p string) string {
		if !filepath.IsAbs(p) || filepath.Dir(p) != dir {
			t.Fatalf("path %q is not in %q", p, dir)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if mcp := read(files.MCPConfig); !strings.Contains(mcp, `"--task-id","42"`) || !strings.Contains(mcp, "/data/tend.db") {
		t.Errorf("mcp config = %s", mcp)
	}
	if hooks := read(files.Settings); !strings.Contains(hooks, "agent-hook") || !strings.Contains(hooks, "/data/tend.db") {
		t.Errorf("settings = %s", hooks)
	}
	if got := read(files.SystemPromptFile); got != "the brief" {
		t.Errorf("brief = %q", got)
	}

	files, err = WriteSessionFiles(dir, "/bin/tend", 42, "/data/tend.db", "")
	if err != nil || files.SystemPromptFile != "" {
		t.Fatalf("no brief: %+v, %v", files, err)
	}
}
