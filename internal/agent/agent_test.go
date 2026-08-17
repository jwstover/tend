package agent

import (
	"regexp"
	"testing"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewSessionID(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if !uuidV4Re.MatchString(id) {
			t.Fatalf("NewSessionID = %q, not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewSessionID produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestLaunchCmd(t *testing.T) {
	c := LaunchCmd("/tmp/work", "abc-123", "fix the bug")
	if c.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", c.Dir)
	}
	want := []string{binary, "--session-id", "abc-123", "-n", "fix the bug"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

func TestResumeCmd(t *testing.T) {
	c := ResumeCmd("/tmp/work", "abc-123")
	if c.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", c.Dir)
	}
	want := []string{binary, "--resume", "abc-123"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
