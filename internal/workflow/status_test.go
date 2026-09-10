package workflow

import (
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func TestRunStateSessionStatus(t *testing.T) {
	cases := []struct {
		state RunState
		want  task.SessionStatus
		ok    bool
	}{
		{RunPending, task.SessionStarting, true},
		{RunRunning, task.SessionWorking, true},
		{RunWaitingReview, task.SessionBlocked, true},
		{RunPaused, task.SessionIdle, true},
		{RunDone, "", false},
		{RunFailed, "", false},
		{RunCancelled, "", false},
		{RunState("bogus"), "", false},
	}
	for _, tc := range cases {
		got, ok := tc.state.SessionStatus()
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s.SessionStatus() = (%q, %v), want (%q, %v)", tc.state, got, ok, tc.want, tc.ok)
		}
	}
}
