package tui

import (
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// A session devhub hosts is in no tmux of tend's, so resuming it here
// would start a second claude on its transcript: tend refuses and says
// where it runs, before looking for claude or tmux at all.
func TestResumeOfAHostedSessionPointsAtItsHost(t *testing.T) {
	sess := task.Session{ExternalID: "ext-h", Host: "devhub"}
	msg := resumeSessionCmd(sess, "/tmp/tend.db", "", "", takeoverRef{})()
	f, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("msg = %T, want statusMsg", msg)
	}
	if !strings.Contains(f.text, "devhub") {
		t.Fatalf("flash %q does not name the host", f.text)
	}
}
