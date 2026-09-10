package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// mustNotChange asserts an idle watcher stays quiet; the negative half of
// every Watcher test, factored out so each one reads as its positive claim.
func mustNotChange(t *testing.T, w *Watcher, when string) {
	t.Helper()
	changed, err := w.Changed(context.Background())
	if err != nil {
		t.Fatalf("Changed %s: %v", when, err)
	}
	if changed {
		t.Errorf("Changed reported true %s; want false", when)
	}
}

func mustChange(t *testing.T, w *Watcher, when string) {
	t.Helper()
	changed, err := w.Changed(context.Background())
	if err != nil {
		t.Fatalf("Changed %s: %v", when, err)
	}
	if !changed {
		t.Errorf("Changed reported false %s; want true", when)
	}
}

// The reason the Watcher exists: a commit from a completely separate
// Store — standing in for another tend process — is noticed, exactly once,
// and an idle database reports nothing.
func TestWatcherSeesCommitsFromAnotherStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tend.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	w, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	mustNotChange(t, w, "on a freshly primed watcher")

	other, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open (second process): %v", err)
	}
	t.Cleanup(func() { other.Close() })
	// Opening the second store ran migrations, which write nothing on an
	// already-migrated database — but goose still touches its version
	// table on some paths, so consume anything that produced before the
	// write under test.
	if _, err := w.Changed(ctx); err != nil {
		t.Fatalf("Changed after second Open: %v", err)
	}

	created, err := other.AddTask(ctx, "written elsewhere")
	if err != nil {
		t.Fatalf("AddTask via other store: %v", err)
	}

	mustChange(t, w, "after a commit from another store")
	mustNotChange(t, w, "on the call after the change was consumed")

	// A second, different kind of write is a second change.
	if err := other.SetState(ctx, created.ID, task.StateDoing); err != nil {
		t.Fatalf("SetState via other store: %v", err)
	}
	mustChange(t, w, "after a state change from another store")
}

// The behavior the TUI leans on: writes through the owning Store's own
// pool are "another connection" too, so the watcher fires for them. That
// means the TUI's own mutations also trigger a (redundant, harmless) live
// reload — documented here so nobody later "fixes" it by trying to filter
// them out at this layer.
func TestWatcherSeesCommitsFromOwningStore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	w, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	mustNotChange(t, w, "before any write")
	mustAdd(t, s, "written through the same store")
	mustChange(t, w, "after a commit through the owning store")
	mustNotChange(t, w, "once consumed")
}

// Tags live in their own table and SetTags does not bump
// tasks.updated_at — one of the reasons data_version, not a timestamp
// column, is the change signal. Make sure such a write is seen.
func TestWatcherSeesTagWrites(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	created := mustAdd(t, s, "tag me")
	w, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	if err := s.SetTags(ctx, created.ID, []string{"live"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	mustChange(t, w, "after SetTags")
}

func TestWatcherCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	w, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
	if _, err := w.Changed(ctx); err == nil {
		t.Error("Changed after Close returned nil error; want an error")
	}
}
