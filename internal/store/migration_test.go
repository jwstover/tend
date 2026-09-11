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
	"github.com/jwstover/tend/internal/workflow"
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

// schemaBeforeWorkflows is the last migration before the workflow tables.
const schemaBeforeWorkflows = 8

// schemaBeforeReview is the last migration before the review state row.
const schemaBeforeReview = 13

// stateOrders reads the states table as name -> sort_order.
func stateOrders(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT name, sort_order FROM states`)
	if err != nil {
		t.Fatalf("reading states: %v", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var order int64
		if err := rows.Scan(&name, &order); err != nil {
			t.Fatalf("scanning states: %v", err)
		}
		out[name] = order
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating states: %v", err)
	}
	return out
}

// Migration 00014 seeds the review state (tend task #199) and renumbers
// the states after it so review sorts between todo and doing. Down has to
// park any review tasks somewhere first or the FK on tasks.state refuses
// the delete, so both directions are driven here.
func TestReviewStateMigrationRoundTrips(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tend.db")

	db := openAt(t, path, schemaBeforeReview)
	if _, err := db.Exec(`INSERT INTO tasks (title, state) VALUES ('in flight', 'doing')`); err != nil {
		t.Fatalf("seeding a task: %v", err)
	}
	provider := providerFor(t, db)

	// Up: review exists, is live, and sits right before doing.
	if _, err := provider.UpTo(ctx, schemaBeforeReview+1); err != nil {
		t.Fatalf("migrating up to 00014: %v", err)
	}
	orders := stateOrders(t, db)
	want := map[string]int64{"inbox": 0, "todo": 1, "review": 2, "doing": 3, "blocked": 4, "done": 5, "someday": 6}
	for name, order := range want {
		if orders[name] != order {
			t.Errorf("after up, %s sort_order = %d, want %d", name, orders[name], order)
		}
	}
	var terminal, hidden int
	if err := db.QueryRowContext(ctx, `SELECT is_terminal, hidden_by_default FROM states WHERE name = 'review'`).Scan(&terminal, &hidden); err != nil {
		t.Fatalf("after up, review row: %v", err)
	}
	if terminal != 0 || hidden != 0 {
		t.Errorf("review is_terminal=%d hidden_by_default=%d, want 0/0 so it shows by default", terminal, hidden)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET state = 'review' WHERE title = 'in flight'`); err != nil {
		t.Fatalf("moving the task to review: %v", err)
	}

	// Down: the row goes, its tasks land in doing, and the numbering is
	// back to the 00001 seed.
	if _, err := provider.DownTo(ctx, schemaBeforeReview); err != nil {
		t.Fatalf("migrating back down to 00013: %v", err)
	}
	orders = stateOrders(t, db)
	if _, still := orders["review"]; still {
		t.Error("after down, the review state row still exists")
	}
	for name, order := range map[string]int64{"inbox": 0, "todo": 1, "doing": 2, "blocked": 3, "done": 4, "someday": 5} {
		if orders[name] != order {
			t.Errorf("after down, %s sort_order = %d, want %d", name, orders[name], order)
		}
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM tasks WHERE title = 'in flight'`).Scan(&state); err != nil {
		t.Fatalf("after down, reading the task: %v", err)
	}
	if state != "doing" {
		t.Errorf("after down, the review task is in %q, want doing", state)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
}

// providerFor builds a goose provider over an already-open handle, for
// tests that need to roll a schema down as well as up.
func providerFor(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(database.DialectSQLite3, db, sub)
	if err != nil {
		t.Fatalf("building provider: %v", err)
	}
	return provider
}

// Migration 00009 adds five tables and a REFERENCES column on
// agent_sessions. Up is exercised by every other store test; this one
// drives it down and back up on a database with existing sessions, since
// ALTER TABLE ... DROP COLUMN has a list of reasons it can refuse (an
// index, a CHECK, a trigger naming the column) and a Down that cannot run
// is a trap discovered only when it is needed.
func TestWorkflowsMigrationRoundTrips(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tend.db")

	db := openAt(t, path, schemaBeforeWorkflows)
	if _, err := db.Exec(`INSERT INTO tasks (title) VALUES ('has a session')`); err != nil {
		t.Fatalf("seeding a task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_sessions (task_id, external_id, cwd, label) VALUES (1, 'sess-1', '/tmp', 'label')`); err != nil {
		t.Fatalf("seeding a session: %v", err)
	}
	provider := providerFor(t, db)

	// Up.
	if _, err := provider.UpTo(ctx, schemaBeforeWorkflows+1); err != nil {
		t.Fatalf("migrating up to 00009: %v", err)
	}
	for _, table := range []string{"workflows", "workflow_steps", "workflow_edges", "workflow_runs", "workflow_step_runs"} {
		if _, err := db.ExecContext(ctx, "SELECT 1 FROM "+table+" LIMIT 1"); err != nil {
			t.Errorf("after up, table %s is missing: %v", table, err)
		}
	}
	var bound sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT workflow_step_run_id FROM agent_sessions WHERE external_id = 'sess-1'`).Scan(&bound); err != nil {
		t.Fatalf("after up, agent_sessions.workflow_step_run_id is missing: %v", err)
	}
	if bound.Valid {
		t.Errorf("a pre-existing session got workflow_step_run_id %d, want NULL", bound.Int64)
	}

	// Down: the column and the tables go, the session stays.
	if _, err := provider.DownTo(ctx, schemaBeforeWorkflows); err != nil {
		t.Fatalf("migrating back down to 00008: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT workflow_step_run_id FROM agent_sessions LIMIT 1`); err == nil {
		t.Error("after down, agent_sessions.workflow_step_run_id still exists")
	}
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM workflows LIMIT 1`); err == nil {
		t.Error("after down, the workflows table still exists")
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_sessions`).Scan(&n); err != nil || n != 1 {
		t.Errorf("after down, agent_sessions holds %d rows (%v), want the 1 seeded", n, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// And up again through the normal path, then use it.
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("re-migrating: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	w, err := s.CreateWorkflow(ctx, "after round trip", "")
	if err != nil {
		t.Fatalf("CreateWorkflow after the round trip: %v", err)
	}
	if _, err := s.AddStep(ctx, w.ID, "step", workflow.StepAgent); err != nil {
		t.Fatalf("AddStep after the round trip: %v", err)
	}
	sessions, err := s.ListSessionsForTask(ctx, 1)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 || sessions[0].StepRunID != nil {
		t.Errorf("seeded session after the round trip = %+v, want one unbound session", sessions)
	}
}

// schemaBeforeParentEvents is the last migration before task_events
// admits the 'parent' kind.
const schemaBeforeParentEvents = 14

// Migration 00015 rebuilds task_events to widen its CHECK, the way 00008
// did for 'project'. It is driven up and down here on a database with
// history: the rebuild must keep every row, the triggers must survive
// being dropped and recreated, the widened CHECK must admit 'parent', and
// Down must drop the rows it can no longer hold rather than fail.
func TestParentEventMigrationRoundTrips(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tend.db")

	db := openAt(t, path, schemaBeforeParentEvents)
	if _, err := db.Exec(`INSERT INTO tasks (title) VALUES ('pre-existing')`); err != nil {
		t.Fatalf("seeding a task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
		VALUES (1, 'pre-existing', 'project', 'Unsorted', 'elsewhere')`); err != nil {
		t.Fatalf("seeding a project event: %v", err)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_events`).Scan(&before); err != nil {
		t.Fatalf("counting seeded events: %v", err)
	}
	if before != 2 {
		t.Fatalf("seeded %d events, want 2 (the created trigger's and the project row)", before)
	}
	// The old CHECK refuses what the migration is about to admit.
	if _, err := db.Exec(`INSERT INTO task_events (task_id, task_title, kind) VALUES (1, 'x', 'parent')`); err == nil {
		t.Fatal("schema 14 accepted a 'parent' event; the test is not exercising the widening")
	}
	provider := providerFor(t, db)

	// Up: rows survive, and the new kind is admitted.
	if _, err := provider.UpTo(ctx, schemaBeforeParentEvents+1); err != nil {
		t.Fatalf("migrating up to 00015: %v", err)
	}
	var after int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events`).Scan(&after); err != nil {
		t.Fatalf("counting migrated events: %v", err)
	}
	if after != before {
		t.Errorf("task_events holds %d rows after the rebuild, want %d", after, before)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
		VALUES (1, 'pre-existing', 'parent', '(top level)', 'host')`); err != nil {
		t.Fatalf("after up, a 'parent' event is refused: %v", err)
	}

	// Down: the parent row goes, everything else stays.
	if _, err := provider.DownTo(ctx, schemaBeforeParentEvents); err != nil {
		t.Fatalf("migrating back down to 00014: %v", err)
	}
	var kinds []string
	rows, err := db.QueryContext(ctx, `SELECT kind FROM task_events ORDER BY id`)
	if err != nil {
		t.Fatalf("reading events after down: %v", err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		kinds = append(kinds, k)
	}
	rows.Close()
	if len(kinds) != 2 || kinds[0] != "created" || kinds[1] != "project" {
		t.Errorf("after down, kinds = %v, want [created project]", kinds)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// And up again through the normal path, then use it end to end.
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("re-migrating: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	host, err := s.AddTask(ctx, "host")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetParent(ctx, 1, &host.ID); err != nil {
		t.Fatalf("SetParent after the round trip: %v", err)
	}
	// The triggers still fire after two rebuilds.
	if err := s.SetState(ctx, host.ID, task.StateDoing); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	events, err := s.ListEvents(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawParent, sawState bool
	for _, ev := range events {
		switch ev.Kind {
		case task.EventParent:
			sawParent = ev.TaskID == 1 && ev.New != nil && *ev.New == "host"
		case task.EventState:
			sawState = sawState || ev.TaskID == host.ID
		}
	}
	if !sawParent || !sawState {
		t.Errorf("after the round trip: parent event=%v state trigger=%v, want both", sawParent, sawState)
	}
}
