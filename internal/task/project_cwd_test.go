package task

import (
	"os"
	"testing"
)

func TestNormalizeProjectCwd(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to expand ~ against")
	}
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"/tmp/app", "/tmp/app"},
		{"  /tmp/app/  ", "/tmp/app"},
		{"/tmp//app/./src/..", "/tmp/app"},
		{"~", home},
		{"~/code/app", home + "/code/app"},
		// A "~" that isn't the home shorthand is left alone: "~user" is
		// someone else's home, and expanding it wrong would be worse than
		// not expanding it.
		{"~other/code", "~other/code"},
		{"relative/dir", "relative/dir"},
	}
	for _, c := range cases {
		if got := NormalizeProjectCwd(c.in); got != c.want {
			t.Errorf("NormalizeProjectCwd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
