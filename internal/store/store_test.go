package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwstover/td/internal/task"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAddTaskDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.AddTask(ctx, "  buy milk ")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if got.ID == 0 {
		t.Error("expected a non-zero id")
	}
	if got.Title != "buy milk" {
		t.Errorf("Title = %q, want %q (normalized)", got.Title, "buy milk")
	}
	if got.State != task.StateInbox {
		t.Errorf("State = %q, want %q", got.State, task.StateInbox)
	}
	if got.BodyMD != "" {
		t.Errorf("BodyMD = %q, want empty", got.BodyMD)
	}
	if got.Project != nil || got.Priority != nil || got.Due != nil || got.SnoozeUntil != nil || got.ParentID != nil || got.CompletedAt != nil {
		t.Error("expected all optional fields to be nil on capture")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("expected timestamps to be set")
	}
}

func TestAddTaskRejectsBlankTitle(t *testing.T) {
	s := newTestStore(t)
	_, err := s.AddTask(context.Background(), "   ")
	if !errors.Is(err, task.ErrEmptyTitle) {
		t.Fatalf("AddTask error = %v, want ErrEmptyTitle", err)
	}
}

func TestListLiveFiltering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	live, err := s.AddTask(ctx, "live one")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	// Drive rows into non-live conditions with raw SQL: the state and
	// snooze mutators arrive in Gate 3, but the live-view query they
	// feed must filter correctly from day one.
	future := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	past := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	fixtures := []struct {
		title  string
		column string
		value  string
		live   bool
	}{
		{"done task", "state", "done", false},
		{"someday task", "state", "someday", false},
		{"snoozed future", "snooze_until", future, false},
		{"snoozed past", "snooze_until", past, true},
	}
	wantTitles := map[string]bool{live.Title: true}
	for _, f := range fixtures {
		created, err := s.AddTask(ctx, f.title)
		if err != nil {
			t.Fatalf("AddTask(%q): %v", f.title, err)
		}
		if _, err := s.db.ExecContext(ctx,
			"UPDATE tasks SET "+f.column+" = ? WHERE id = ?", f.value, created.ID,
		); err != nil {
			t.Fatalf("fixture update for %q: %v", f.title, err)
		}
		if f.live {
			wantTitles[f.title] = true
		}
	}

	got, err := s.ListLive(ctx)
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	gotTitles := map[string]bool{}
	for _, tk := range got {
		gotTitles[tk.Title] = true
	}
	if len(gotTitles) != len(wantTitles) {
		t.Errorf("ListLive returned %v, want titles %v", gotTitles, wantTitles)
	}
	for title := range wantTitles {
		if !gotTitles[title] {
			t.Errorf("ListLive missing %q", title)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "td.db")
	ctx := context.Background()

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.AddTask(ctx, "persists"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	s1.Close()

	// Reopening must rerun migrations as a no-op and see existing data.
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	got, err := s2.ListLive(ctx)
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if len(got) != 1 || got[0].Title != "persists" {
		t.Fatalf("ListLive after reopen = %+v, want the one persisted task", got)
	}
}
