package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/jwstover/tend/internal/task"
)

// schemaBeforeProjects is the last migration that predates projects and
// tags. Seeding at this version is what makes the test a real migration
// test rather than a test of the current schema.
const schemaBeforeProjects = 6

// openAt opens a database migrated only as far as version, so a test can
// seed rows in an older schema shape.
func openAt(t *testing.T, path string, version int64) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		url.PathEscape(path),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(database.DialectSQLite3, db, sub)
	if err != nil {
		t.Fatalf("building provider: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), version); err != nil {
		t.Fatalf("migrating to %d: %v", version, err)
	}
	return db
}

// TestMigrationMovesProjectStringsToTags is the acceptance test for
// migration 00007 (docs/projects-plan.md §2.2). It seeds a database in the
// schema that shipped before projects existed, migrates it, and asserts
// the old flat `tasks.project` strings survive as tags — the one step in
// this feature that can destroy real user data, since it ends in
// ALTER TABLE tasks DROP COLUMN project.
func TestMigrationMovesProjectStringsToTags(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tend.db")

	db := openAt(t, path, schemaBeforeProjects)
	// Rows in the OLD shape: a plain value, a NULL, a whitespace-only
	// value, and a case variant that the new NOCASE unique index folds
	// into an existing tag.
	if _, err := db.Exec(`INSERT INTO tasks (title, project) VALUES
		('has a project', 'tend'),
		('no project',     NULL),
		('blank project',  '   '),
		('padded project', '  home  '),
		('case variant',   'HOME')`); err != nil {
		t.Fatalf("seeding old-schema rows: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seeded db: %v", err)
	}

	// Open runs the remaining migrations, 00007 among them.
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrating seeded db: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	byTitle := map[string]int64{}
	tasks, err := s.ListLive(ctx, nil)
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	for _, tk := range tasks {
		byTitle[tk.Title] = tk.ID
	}
	if len(byTitle) != 5 {
		t.Fatalf("migrated %d tasks, want 5: %v", len(byTitle), byTitle)
	}

	tagsFor := func(title string) []string {
		t.Helper()
		got, err := s.TagsForTask(ctx, byTitle[title])
		if err != nil {
			t.Fatalf("TagsForTask(%q): %v", title, err)
		}
		return got
	}

	if got := tagsFor("has a project"); len(got) != 1 || got[0] != "tend" {
		t.Errorf("tags for %q = %v, want [tend]", "has a project", got)
	}
	if got := tagsFor("no project"); len(got) != 0 {
		t.Errorf("tags for a NULL project = %v, want none", got)
	}
	if got := tagsFor("blank project"); len(got) != 0 {
		t.Errorf("tags for a whitespace-only project = %v, want none", got)
	}
	// The value was stored padded; trim() in the migration is what makes
	// this "home" rather than a distinct "  home  " tag.
	if got := tagsFor("padded project"); len(got) != 1 || got[0] != "home" {
		t.Errorf("tags for a padded project = %v, want [home]", got)
	}
	// 'HOME' collides with 'home' under NOCASE, so it attaches to the same
	// tag row; which spelling won the insert race is not worth asserting.
	if got := tagsFor("case variant"); len(got) != 1 || !strings.EqualFold(got[0], "home") {
		t.Errorf("tags for a case variant = %v, want the existing home tag", got)
	}

	// Two tag rows, not three: 'HOME' folded into 'home'.
	allTags, err := s.ListTags(ctx)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(allTags) != 2 {
		t.Errorf("ListTags = %v, want 2 tags (tend + home)", allTags)
	}

	// Every migrated task lands in the seeded default project: decision 3
	// moves the old strings to tags only, it does not infer projects.
	for title, id := range byTitle {
		got, err := s.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask(%q): %v", title, err)
		}
		if got.ProjectID != task.DefaultProjectID {
			t.Errorf("%q ProjectID = %d, want the default project %d",
				title, got.ProjectID, task.DefaultProjectID)
		}
	}

	// And the old column is really gone, not merely unused.
	if _, err := s.db.ExecContext(ctx, "SELECT project FROM tasks LIMIT 1"); err == nil {
		t.Error("tasks.project still exists; the migration did not drop it")
	}
}

// The seeded default project has to exist on a brand-new database too, or
// every capture path that falls back to it breaks.
func TestFreshDatabaseHasDefaultProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.GetProject(ctx, task.DefaultProjectID)
	if err != nil {
		t.Fatalf("GetProject(default): %v", err)
	}
	if p.Name != "Unsorted" {
		t.Errorf("default project name = %q, want Unsorted", p.Name)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("ListProjects on a fresh db = %v, want just the default", projects)
	}
}

// schemaBeforeProjectEvents is the last migration before task_events
// could hold a project move.
const schemaBeforeProjectEvents = 7

// Migration 00008 rebuilds task_events to widen a CHECK constraint, which
// means dropping and recreating the table and the three triggers that
// write to it. This asserts the rebuild is lossless and the triggers come
// back working -- a rebuild that silently dropped history would be the
// worst kind of quiet failure.
func TestTaskEventsRebuildKeepsHistoryAndTriggers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tend.db")

	db := openAt(t, path, schemaBeforeProjectEvents)
	if _, err := db.Exec(`INSERT INTO tasks (title) VALUES ('pre-existing')`); err != nil {
		t.Fatalf("seeding a task: %v", err)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_events`).Scan(&before); err != nil {
		t.Fatalf("counting seeded events: %v", err)
	}
	if before == 0 {
		t.Fatal("expected the created trigger to have written an event")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seeded db: %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrating seeded db: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	var after int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events`).Scan(&after); err != nil {
		t.Fatalf("counting migrated events: %v", err)
	}
	if after != before {
		t.Errorf("task_events holds %d rows after the rebuild, want the %d it had", after, before)
	}

	// The triggers have to survive being dropped and recreated.
	created, err := s.AddTask(ctx, "written after the rebuild")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetState(ctx, created.ID, task.StateDoing); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	events, err := s.ListEvents(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawCreated, sawState bool
	for _, ev := range events {
		if ev.TaskID != created.ID {
			continue
		}
		switch ev.Kind {
		case task.EventCreated:
			sawCreated = true
		case task.EventState:
			sawState = true
		}
	}
	if !sawCreated || !sawState {
		t.Errorf("triggers did not survive the rebuild: created=%v state=%v", sawCreated, sawState)
	}

	// ...and the widened CHECK admits the new kind.
	if err := s.SetProject(ctx, created.ID, task.DefaultProjectID); err != nil {
		t.Fatalf("SetProject (no-op): %v", err)
	}
	p, err := s.CreateProject(ctx, "somewhere else")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.SetProject(ctx, created.ID, p.ID); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
}
