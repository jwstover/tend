package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// A hosted session reads back with its host, and only a hosted row still
// 'starting' can be taken back: once its hooks report, or for a session
// tend launched, the delete is a no-op.
func TestHostedSessionLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	parent := mustAdd(t, s, "fix the bug")

	sess, err := s.CreateHostedSession(ctx, parent.ID, "ext-h", "/tmp/work", parent.Title, "devhub")
	if err != nil {
		t.Fatalf("CreateHostedSession: %v", err)
	}
	if sess.Host != "devhub" || sess.TmuxSession != "" || sess.Status != task.SessionStarting {
		t.Fatalf("got host %q tmux %q status %q", sess.Host, sess.TmuxSession, sess.Status)
	}
	got, err := s.SessionByExternalID(ctx, "ext-h")
	if err != nil || got.ID != sess.ID || got.Host != "devhub" {
		t.Fatalf("SessionByExternalID = %+v, %v", got, err)
	}
	if _, err := s.SessionByExternalID(ctx, "nope"); !errors.Is(err, task.ErrSessionNotFound) {
		t.Fatalf("missing session: err = %v, want ErrSessionNotFound", err)
	}

	if _, err := s.CreateSession(ctx, parent.ID, "ext-t", "/tmp/work", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if ok, err := s.DeleteHostedStartingSession(ctx, "ext-t"); err != nil || ok {
		t.Fatalf("delete of a tend session: ok %v, err %v; want false", ok, err)
	}

	if err := s.SetSessionStatus(ctx, "ext-h", task.SessionIdle); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}
	if ok, _ := s.DeleteHostedStartingSession(ctx, "ext-h"); ok {
		t.Fatal("deleted a hosted session that had reported idle")
	}

	if _, err := s.CreateHostedSession(ctx, parent.ID, "ext-h2", "/tmp/work", parent.Title, "devhub"); err != nil {
		t.Fatalf("CreateHostedSession: %v", err)
	}
	if ok, err := s.DeleteHostedStartingSession(ctx, "ext-h2"); err != nil || !ok {
		t.Fatalf("delete of a starting hosted session: ok %v, err %v; want true", ok, err)
	}
}

// A sub-task of a task that doesn't exist is ErrTaskNotFound, not a bare
// "no rows".
func TestAddChildOfMissingParent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddChild(context.Background(), 999, "orphan"); !errors.Is(err, task.ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
}

// A taken project name, in any case, says so rather than quoting SQLite.
func TestCreateProjectNameTaken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateProject(ctx, "garden"); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateProject(ctx, "Garden")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want one saying it already exists", err)
	}
}
