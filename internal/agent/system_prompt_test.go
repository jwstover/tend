package agent

import (
	"os"
	"strings"
	"testing"
)

// The block lands in the file byte for byte, however long, and the
// cleanup takes the file away.
func TestWriteSystemPromptRoundTripsAndCleansUp(t *testing.T) {
	text := "bound to task #4\n\n" + strings.Repeat("a long body line with \"quotes\", `ticks` and $HOME\n", 2000)
	path, cleanup, err := WriteSystemPrompt(text)
	if err != nil {
		t.Fatalf("WriteSystemPrompt: %v", err)
	}
	if path == "" {
		t.Fatal("WriteSystemPrompt returned no path")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != text {
		t.Errorf("file content differs from the block (len %d vs %d)", len(got), len(text))
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s behind (stat err %v)", path, err)
	}
}

// An empty block is "no brief": nothing is written and the caller can pass
// the empty path straight into LaunchOpts, where it adds no flag.
func TestWriteSystemPromptEmptyWritesNothing(t *testing.T) {
	path, cleanup, err := WriteSystemPrompt("")
	if err != nil {
		t.Fatalf("WriteSystemPrompt(\"\"): %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	cleanup() // must be safe to call
}
