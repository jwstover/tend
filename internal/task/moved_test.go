package task

import "testing"

func projectEvent(id int64, title, from, to string) Event {
	return Event{TaskID: id, TaskTitle: title, Kind: EventProject, Old: &from, New: &to}
}

func TestSummarizeReportsProjectMoves(t *testing.T) {
	sum := Summarize([]Event{projectEvent(1, "move me", "Unsorted", "tend")})
	if len(sum.Moved) != 1 {
		t.Fatalf("Moved = %+v, want one entry", sum.Moved)
	}
	got := sum.Moved[0]
	if got.TaskID != 1 || got.Title != "move me" || got.To != "tend" {
		t.Errorf("Moved[0] = %+v, want task 1 move me -> tend", got)
	}
}

// A task moved twice in the window reports where it ended up, not its
// route -- the same "replay to the destination" rule state events follow.
func TestSummarizeReportsTheLastMove(t *testing.T) {
	sum := Summarize([]Event{
		projectEvent(1, "wanderer", "Unsorted", "tend"),
		projectEvent(1, "wanderer", "tend", "hapi"),
	})
	if len(sum.Moved) != 1 || sum.Moved[0].To != "hapi" {
		t.Errorf("Moved = %+v, want a single entry landing in hapi", sum.Moved)
	}
}

// Moving and completing in one window are both worth reporting, so Moved
// is independent of the completed/blocked/started precedence.
func TestSummarizeMovedIsIndependentOfState(t *testing.T) {
	sum := Summarize([]Event{
		projectEvent(1, "both", "Unsorted", "tend"),
		stateEvent(1, "both", "doing", "done"),
	})
	if len(sum.Completed) != 1 {
		t.Errorf("Completed = %+v, want the task", sum.Completed)
	}
	if len(sum.Moved) != 1 {
		t.Errorf("Moved = %+v, want the task", sum.Moved)
	}
}

// A window holding only a move is not empty: it has something to report.
func TestSummaryEmptyAccountsForMoves(t *testing.T) {
	if (Summary{}).Empty() != true {
		t.Error("a blank summary should be empty")
	}
	sum := Summarize([]Event{projectEvent(1, "moved", "Unsorted", "tend")})
	if sum.Empty() {
		t.Error("a summary holding a move should not be empty")
	}
}
