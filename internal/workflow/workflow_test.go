package workflow

import (
	"errors"
	"testing"
)

func TestNormalizeOutcome(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  error
	}{
		{"approve", "approve", nil},
		{"  Approve  ", "approve", nil},
		{"REJECT", "reject", nil},
		{"", "", ErrEmptyOutcome},
		{"   ", "", ErrEmptyOutcome},
	}
	for _, c := range cases {
		got, err := NormalizeOutcome(c.in)
		if !errors.Is(err, c.err) {
			t.Errorf("NormalizeOutcome(%q) err = %v, want %v", c.in, err, c.err)
		}
		if got != c.want {
			t.Errorf("NormalizeOutcome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	if got, err := NormalizeName("  Ship it "); err != nil || got != "Ship it" {
		t.Errorf("NormalizeName = (%q, %v), want (%q, nil): case is preserved, whitespace trimmed", got, err, "Ship it")
	}
	if _, err := NormalizeName(" \t"); !errors.Is(err, ErrEmptyName) {
		t.Errorf("NormalizeName(blank) = %v, want ErrEmptyName", err)
	}
}

func TestRunStateTerminalAndValid(t *testing.T) {
	terminal := map[RunState]bool{
		RunPending: false, RunRunning: false, RunWaitingReview: false, RunPaused: false,
		RunDone: true, RunFailed: true, RunCancelled: true,
	}
	for st, want := range terminal {
		if !st.Valid() {
			t.Errorf("%s should be a valid state", st)
		}
		if st.Terminal() != want {
			t.Errorf("%s.Terminal() = %v, want %v", st, st.Terminal(), want)
		}
	}
	if RunState("bogus").Valid() {
		t.Error("an unknown state must not be valid")
	}
}

func TestStepKindValid(t *testing.T) {
	if !StepAgent.Valid() || !StepGate.Valid() {
		t.Error("agent and gate are the two kinds")
	}
	if StepKind("human").Valid() {
		t.Error("an unknown kind must not be valid")
	}
}

func TestInUseErrorMatchesSentinel(t *testing.T) {
	err := InUseError("workflow 3", 7)
	if !errors.Is(err, ErrInUse) {
		t.Errorf("InUseError does not match ErrInUse: %v", err)
	}
	if want := "workflow 3 is referenced by an active run 7"; err.Error() != want {
		t.Errorf("InUseError message = %q, want %q", err.Error(), want)
	}
}
