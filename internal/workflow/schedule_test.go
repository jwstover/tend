package workflow

import "testing"

func TestScheduleWarnings(t *testing.T) {
	steps := []Step{
		{Name: "a", Kind: StepAgent, PermissionMode: "bypassPermissions"},
		{Name: "b", Kind: StepAgent, PermissionMode: "acceptEdits"},
		{Name: "c", Kind: StepGate, PermissionMode: "bypassPermissions"},
	}
	got := ScheduleWarnings(steps)
	if len(got) != 1 || got[0][:8] != `step "a"` {
		t.Errorf("ScheduleWarnings = %q, want one warning for step a", got)
	}
}
