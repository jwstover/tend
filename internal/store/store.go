// Package store is the persistence layer: the only package that touches
// SQL or the sqlc-generated code. It returns domain types from
// internal/task.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	_ "modernc.org/sqlite"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
)

//go:generate sqlc -f ../../sqlc.yaml generate

//go:embed migrations/*.sql
var migrationsFS embed.FS

// sqliteTimeLayout is the format datetime('now') writes (UTC, no zone).
const sqliteTimeLayout = "2006-01-02 15:04:05"

// Store wraps the sqlc-generated Queries, owns the DB handle and
// transactions, and translates between gen rows and task domain types.
type Store struct {
	db *sql.DB
	q  *gen.Queries
}

// Open creates the parent directory if needed, opens the SQLite file in
// WAL mode, applies pending migrations, and returns a ready Store.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		url.PathEscape(path),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening db %s: %w", path, err)
	}

	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating db %s: %w", path, err)
	}

	return &Store{db: db, q: gen.New(db)}, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

func migrate(ctx context.Context, db *sql.DB) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(database.DialectSQLite3, db, sub)
	if err != nil {
		return fmt.Errorf("building migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// AddTask captures a task into the default project: a bare title,
// everything else defaulted by the schema (state inbox, empty body).
//
// Deliberately not "the active project". A capture with no project named
// lands somewhere fixed and predictable, so `tend add` from any shell
// behaves the same way every time; callers that know where a task belongs
// (the TUI, `tend add -p`) say so through AddTaskIn. This is the same
// principle as AGENTS.md §2 -- capture requires nothing and organization
// is a later, separate act -- applied to the project as well as the state.
func (s *Store) AddTask(ctx context.Context, title string) (task.Task, error) {
	return s.AddTaskIn(ctx, task.DefaultProjectID, title)
}

// AddTaskIn captures a task into a named project, for `tend add -p` and
// the TUI's quick-add against the selected project.
func (s *Store) AddTaskIn(ctx context.Context, projectID int64, title string) (task.Task, error) {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return task.Task{}, err
	}
	row, err := s.q.CreateTask(ctx, gen.CreateTaskParams{Title: t, ProjectID: projectID})
	if err != nil {
		return task.Task{}, fmt.Errorf("inserting task: %w", err)
	}
	return toDomain(row)
}

// AddTaskWithBody captures a task that arrives with context already
// attached (e.g. an imported Jira ticket): title plus a markdown body,
// everything else defaulted by the schema.
func (s *Store) AddTaskWithBody(ctx context.Context, title, body string) (task.Task, error) {
	return s.AddTaskWithBodyIn(ctx, task.DefaultProjectID, title, body)
}

// AddTaskWithBodyIn is AddTaskWithBody against an explicit project.
func (s *Store) AddTaskWithBodyIn(ctx context.Context, projectID int64, title, body string) (task.Task, error) {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return task.Task{}, err
	}
	row, err := s.q.CreateTaskWithBody(ctx, gen.CreateTaskWithBodyParams{
		Title: t, BodyMd: body, ProjectID: projectID,
	})
	if err != nil {
		return task.Task{}, fmt.Errorf("inserting task: %w", err)
	}
	return toDomain(row)
}

// ListLive returns the live view: non-terminal, non-hidden states, and
// not snoozed into the future. A nil projectID means every project (the
// projects column's All row); otherwise the view is scoped to that one.
func (s *Store) ListLive(ctx context.Context, projectID *int64) ([]task.Task, error) {
	rows, err := s.q.ListLiveTasks(ctx, projectFilter(projectID))
	if err != nil {
		return nil, fmt.Errorf("listing live tasks: %w", err)
	}
	return toDomainSlice(rows)
}

// ListLiveWithCompleted is ListLive plus the completed (done) tasks, for
// when the list view has the completed section toggled on.
func (s *Store) ListLiveWithCompleted(ctx context.Context, projectID *int64) ([]task.Task, error) {
	rows, err := s.q.ListLiveWithCompletedTasks(ctx, projectFilter(projectID))
	if err != nil {
		return nil, fmt.Errorf("listing live tasks with completed: %w", err)
	}
	return toDomainSlice(rows)
}

// AddChild captures a sub-task under an existing task.
func (s *Store) AddChild(ctx context.Context, parentID int64, title string) (task.Task, error) {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return task.Task{}, err
	}
	// The child's project_id is copied off the parent by the query
	// itself, so a sub-task can never sit in a different project.
	row, err := s.q.CreateChildTask(ctx, gen.CreateChildTaskParams{
		Title:    t,
		ParentID: parentID,
	})
	if err != nil {
		return task.Task{}, fmt.Errorf("inserting sub-task of %d: %w", parentID, err)
	}
	return toDomain(row)
}

// ListInbox returns every task still in the inbox state, oldest first.
// This feeds the triage view.
func (s *Store) ListInbox(ctx context.Context, projectID *int64) ([]task.Task, error) {
	rows, err := s.q.ListInboxTasks(ctx, projectFilter(projectID))
	if err != nil {
		return nil, fmt.Errorf("listing inbox tasks: %w", err)
	}
	return toDomainSlice(rows)
}

// ListChildren returns the sub-tasks of a task, oldest first.
func (s *Store) ListChildren(ctx context.Context, parentID int64) ([]task.Task, error) {
	rows, err := s.q.ListChildTasks(ctx, sql.NullInt64{Int64: parentID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("listing children of task %d: %w", parentID, err)
	}
	return toDomainSlice(rows)
}

// ChildCounts returns per-parent sub-task done/total counts for every
// task that has children. Done children are filtered out of the live
// view, so the list derives its N/M progress from this instead.
func (s *Store) ChildCounts(ctx context.Context) (map[int64]task.ChildCount, error) {
	rows, err := s.q.ListChildCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing child counts: %w", err)
	}
	counts := make(map[int64]task.ChildCount, len(rows))
	for _, r := range rows {
		if r.ParentID.Valid {
			counts[r.ParentID.Int64] = task.ChildCount{Done: r.Done, Total: r.Total}
		}
	}
	return counts, nil
}

// SessionStatuses returns the status of each task's most-recently-active
// agent session, keyed by task id — what the list row renders so a
// background session's state is visible without opening the task
// (docs/agent-sessions-plan.md 8.4).
//
// The query returns every session oldest-first and the map keeps the
// last write per task, which is the most recent one. That's cheaper to
// reason about than a correlated MAX() subquery and needs no tiebreak
// rule for two sessions sharing a timestamp, since the ordering already
// falls back to id.
func (s *Store) SessionStatuses(ctx context.Context) (map[int64]task.SessionStatus, error) {
	rows, err := s.q.ListSessionStatuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing session statuses: %w", err)
	}
	statuses := make(map[int64]task.SessionStatus, len(rows))
	for _, r := range rows {
		statuses[r.TaskID] = task.SessionStatus(r.Status)
	}
	return statuses, nil
}

// CountInbox returns the number of tasks awaiting triage.
func (s *Store) CountInbox(ctx context.Context, projectID *int64) (int64, error) {
	n, err := s.q.CountInboxTasks(ctx, projectFilter(projectID))
	if err != nil {
		return 0, fmt.Errorf("counting inbox tasks: %w", err)
	}
	return n, nil
}

// SetState moves a task to a new workflow state. Entering done stamps
// completed_at; entering any other state clears it.
func (s *Store) SetState(ctx context.Context, id int64, st task.State) error {
	if !st.Valid() {
		return fmt.Errorf("unknown state %q", st)
	}
	err := s.q.SetTaskState(ctx, gen.SetTaskStateParams{State: string(st), ID: id})
	if err != nil {
		return fmt.Errorf("setting task %d state to %s: %w", id, st, err)
	}
	return nil
}

// SetProject moves a task, and its whole sub-tree, into a project. The
// sub-tree goes along because a child sitting in a different project from
// its parent is incoherent.
func (s *Store) SetProject(ctx context.Context, taskID, projectID int64) error {
	return s.inTx(ctx, func(q *gen.Queries) error {
		ids, err := subtreeIDs(ctx, q, taskID)
		if err != nil {
			return err
		}
		if err := q.SetTasksProject(ctx, gen.SetTasksProjectParams{
			ProjectID: projectID,
			Ids:       ids,
		}); err != nil {
			return fmt.Errorf("moving task %d to project %d: %w", taskID, projectID, err)
		}
		return nil
	})
}

// subtreeIDs collects a task and every descendant, breadth-first. It
// stands in for the recursive CTE sqlc v1.31.1 cannot parse (see
// queries/tasks.sql). The seen set is a cheap guard: parent_id cannot
// cycle in practice, but this walks database rows, and a cycle would
// otherwise loop forever.
func subtreeIDs(ctx context.Context, q *gen.Queries, root int64) ([]int64, error) {
	ids := []int64{root}
	seen := map[int64]bool{root: true}
	for i := 0; i < len(ids); i++ {
		kids, err := q.ListChildIDs(ctx, sql.NullInt64{Int64: ids[i], Valid: true})
		if err != nil {
			return nil, fmt.Errorf("listing children of task %d: %w", ids[i], err)
		}
		for _, k := range kids {
			if !seen[k] {
				seen[k] = true
				ids = append(ids, k)
			}
		}
	}
	return ids, nil
}

// SetPriority assigns a priority (1 = highest .. 4) to a task; nil clears it.
func (s *Store) SetPriority(ctx context.Context, id int64, p *int64) error {
	if p != nil && (*p < task.PriorityHighest || *p > task.PriorityLowest) {
		return fmt.Errorf("priority %d out of range %d..%d", *p, task.PriorityHighest, task.PriorityLowest)
	}
	err := s.q.SetTaskPriority(ctx, gen.SetTaskPriorityParams{
		Priority: toNullInt64(p),
		ID:       id,
	})
	if err != nil {
		return fmt.Errorf("setting task %d priority: %w", id, err)
	}
	return nil
}

// SetDue assigns a due date (ISO 8601, validated) to a task; nil clears it.
func (s *Store) SetDue(ctx context.Context, id int64, due *string) error {
	var v *string
	if due != nil {
		d, err := task.NormalizeDate(*due)
		if err != nil {
			return err
		}
		v = &d
	}
	err := s.q.SetTaskDue(ctx, gen.SetTaskDueParams{Due: toNullString(v), ID: id})
	if err != nil {
		return fmt.Errorf("setting task %d due date: %w", id, err)
	}
	return nil
}

// SetTitle renames a task; the new title is trimmed and blanks rejected,
// the same validation capture uses.
func (s *Store) SetTitle(ctx context.Context, id int64, title string) error {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return err
	}
	if err := s.q.SetTaskTitle(ctx, gen.SetTaskTitleParams{Title: t, ID: id}); err != nil {
		return fmt.Errorf("setting task %d title: %w", id, err)
	}
	return nil
}

// SetBody replaces a task's markdown body.
func (s *Store) SetBody(ctx context.Context, id int64, body string) error {
	if err := s.q.SetTaskBody(ctx, gen.SetTaskBodyParams{BodyMd: body, ID: id}); err != nil {
		return fmt.Errorf("setting task %d body: %w", id, err)
	}
	return nil
}

// DeleteTask removes a task. Its sub-tasks cascade away with it (the
// parent_id foreign key is ON DELETE CASCADE).
func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	if err := s.q.DeleteTask(ctx, id); err != nil {
		return fmt.Errorf("deleting task %d: %w", id, err)
	}
	return nil
}

// AddLogEntry captures a standup note, optionally attached to a task.
func (s *Store) AddLogEntry(ctx context.Context, taskID *int64, body string) (task.LogEntry, error) {
	b, err := task.NormalizeNote(body)
	if err != nil {
		return task.LogEntry{}, err
	}
	row, err := s.q.CreateLogEntry(ctx, gen.CreateLogEntryParams{
		TaskID: toNullInt64(taskID),
		Body:   b,
	})
	if err != nil {
		return task.LogEntry{}, fmt.Errorf("inserting log entry: %w", err)
	}
	return logToDomain(row)
}

// ListTaskLog returns every note attached to a task, newest first —
// the per-task history the detail pane shows.
func (s *Store) ListTaskLog(ctx context.Context, taskID int64) ([]task.LogEntry, error) {
	rows, err := s.q.ListLogEntriesForTask(ctx, sql.NullInt64{Int64: taskID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("listing log for task %d: %w", taskID, err)
	}
	notes := make([]task.LogEntry, 0, len(rows))
	for _, row := range rows {
		n, err := logToDomain(row)
		if err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, nil
}

// ListLogEntries returns the standup notes captured in [from, to],
// oldest first. Bounds convert like ListEvents.
func (s *Store) ListLogEntries(ctx context.Context, from, to time.Time) ([]task.LogEntry, error) {
	rows, err := s.q.ListLogEntriesBetween(ctx, gen.ListLogEntriesBetweenParams{
		StartAt: from.UTC().Format(sqliteTimeLayout),
		EndAt:   to.UTC().Format(sqliteTimeLayout),
	})
	if err != nil {
		return nil, fmt.Errorf("listing log entries: %w", err)
	}
	notes := make([]task.LogEntry, 0, len(rows))
	for _, row := range rows {
		n, err := logToDomain(gen.LogEntry{
			ID: row.ID, TaskID: row.TaskID, Body: row.Body, CreatedAt: row.CreatedAt,
		})
		if err != nil {
			return nil, err
		}
		n.TaskTitle = row.TaskTitle
		notes = append(notes, n)
	}
	return notes, nil
}

// ListEvents returns the activity-log events recorded in [from, to],
// oldest first. Bounds are converted to the UTC layout the DB stores;
// the end is inclusive because timestamps have second granularity and
// an event written in the same second as the query must not be lost.
func (s *Store) ListEvents(ctx context.Context, from, to time.Time) ([]task.Event, error) {
	rows, err := s.q.ListEventsBetween(ctx, gen.ListEventsBetweenParams{
		StartAt: from.UTC().Format(sqliteTimeLayout),
		EndAt:   to.UTC().Format(sqliteTimeLayout),
	})
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	events := make([]task.Event, 0, len(rows))
	for _, row := range rows {
		ev, err := eventToDomain(row)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// CreateSession records a Claude Code session launched or resumed against
// a task: the pinned external session id, the directory it ran in, and
// the task's title snapshotted as the label. tmuxSession is the wrapping
// tmux session's name, or "" when the session wasn't launched under tmux
// (see task.Session).
func (s *Store) CreateSession(ctx context.Context, taskID int64, externalID, cwd, label, tmuxSession string) (task.Session, error) {
	row, err := s.q.CreateSession(ctx, gen.CreateSessionParams{
		TaskID: taskID, ExternalID: externalID, Cwd: cwd, Label: label, TmuxSession: tmuxSession,
	})
	if err != nil {
		return task.Session{}, fmt.Errorf("inserting session for task %d: %w", taskID, err)
	}
	return sessionToDomain(row)
}

// ListSessionsForTask returns a task's sessions, most recently active
// first — the resume picker's source list.
func (s *Store) ListSessionsForTask(ctx context.Context, taskID int64) ([]task.Session, error) {
	rows, err := s.q.ListSessionsForTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing sessions for task %d: %w", taskID, err)
	}
	sessions := make([]task.Session, 0, len(rows))
	for _, row := range rows {
		sess, err := sessionToDomain(row)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// TouchSession bumps a session's last-active timestamp after a resume.
func (s *Store) TouchSession(ctx context.Context, id int64) error {
	if err := s.q.TouchSession(ctx, id); err != nil {
		return fmt.Errorf("touching session %d: %w", id, err)
	}
	return nil
}

// UpdateSessionLabel replaces a session's label with a short description
// of what it actually did, derived from the post-session recap call
// (see docs/agent-sessions-plan.md §5) — the auto-naming fix for the
// static task-title snapshot not distinguishing sessions on the same
// task. Keyed by external_id rather than the row id: it's the identifier
// recapSessionCmd already has in hand for both a freshly launched
// session (whose row id it never sees, since CreateSession's caller
// discards the result) and a resumed one alike.
func (s *Store) UpdateSessionLabel(ctx context.Context, externalID, label string) error {
	if err := s.q.UpdateSessionLabel(ctx, gen.UpdateSessionLabelParams{ExternalID: externalID, Label: label}); err != nil {
		return fmt.Errorf("updating session label for %s: %w", externalID, err)
	}
	return nil
}

// SetSessionNeedsRecap flags a session as still owing a recap, set when
// a session is backgrounded rather than exited so the recap call is
// deliberately skipped while it's live (docs/agent-sessions-plan.md
// §8.1). Keyed by external_id for the same reason UpdateSessionLabel is:
// it's what the TUI has in hand on both the launch and resume paths.
// Nothing clears the flag yet — Phase 4.2's SessionEnd hook drains it.
func (s *Store) SetSessionNeedsRecap(ctx context.Context, externalID string, needs bool) error {
	var v int64
	if needs {
		v = 1
	}
	if err := s.q.SetSessionNeedsRecap(ctx, gen.SetSessionNeedsRecapParams{ExternalID: externalID, NeedsRecap: v}); err != nil {
		return fmt.Errorf("setting needs_recap for %s: %w", externalID, err)
	}
	return nil
}

// SetSessionStatus records what a session's Claude Code hooks last
// reported about it (docs/agent-sessions-plan.md §8.2), keyed by
// external_id — the id the hook payload carries as session_id, which is
// what makes the correlation a lookup rather than a join.
//
// A session id with no row is not an error: agent_sessions rows are only
// written once a session's first terminal handoff *returns*, so hooks
// fired during a brand-new session's first run legitimately match
// nothing. The UPDATE affects zero rows and that's the whole story.
//
// last_active_at is bumped alongside the status because a hook event is
// genuine activity — it keeps the resume picker's newest-first ordering
// honest for a session that's been running in the background.
func (s *Store) SetSessionStatus(ctx context.Context, externalID string, status task.SessionStatus) error {
	err := s.q.SetSessionStatus(ctx, gen.SetSessionStatusParams{
		Status: string(status), ExternalID: externalID,
	})
	if err != nil {
		return fmt.Errorf("setting status for session %s: %w", externalID, err)
	}
	return nil
}

// ListSessionsNeedingRecap returns every session still owing a recap:
// one that was backgrounded rather than exited, so the recap call was
// deliberately skipped while it was live (§8.1).
//
// Deliberately not filtered to status = 'ended'. A host that dies takes
// its tmux server and its chance to fire SessionEnd with it, and such a
// session would be stranded forever by a status filter. The caller
// decides liveness with `tmux has-session`, which is authoritative in
// every case — including /clear, which fires SessionEnd while the
// process keeps running.
func (s *Store) ListSessionsNeedingRecap(ctx context.Context) ([]task.Session, error) {
	rows, err := s.q.ListSessionsNeedingRecap(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing sessions needing recap: %w", err)
	}
	sessions := make([]task.Session, 0, len(rows))
	for _, row := range rows {
		sess, err := sessionToDomain(row)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// ClaimSessionRecap atomically takes ownership of a session's owed
// recap, reporting whether this caller is the one that got it. The
// UPDATE clears needs_recap only if it was still set, so of two tend
// instances draining the same debt concurrently exactly one sees true
// and fires the (expensive, and side-effecting) recap call.
//
// Claiming before running the recap rather than after means a recap that
// then fails loses the debt. That's the intended trade: it bounds the
// work to one attempt instead of retrying forever, and per §5's existing
// convention a lost automatic recap isn't something the user can act on.
func (s *Store) ClaimSessionRecap(ctx context.Context, externalID string) (bool, error) {
	n, err := s.q.ClaimSessionRecap(ctx, externalID)
	if err != nil {
		return false, fmt.Errorf("claiming recap for session %s: %w", externalID, err)
	}
	return n > 0, nil
}

// GetTask loads a single task by id.
func (s *Store) GetTask(ctx context.Context, id int64) (task.Task, error) {
	row, err := s.q.GetTask(ctx, id)
	if err != nil {
		return task.Task{}, fmt.Errorf("loading task %d: %w", id, err)
	}
	return toDomain(row)
}

func toDomainSlice(rows []gen.Task) ([]task.Task, error) {
	tasks := make([]task.Task, 0, len(rows))
	for _, row := range rows {
		t, err := toDomain(row)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func toDomain(row gen.Task) (task.Task, error) {
	created, err := parseTime(row.CreatedAt)
	if err != nil {
		return task.Task{}, fmt.Errorf("task %d created_at: %w", row.ID, err)
	}
	updated, err := parseTime(row.UpdatedAt)
	if err != nil {
		return task.Task{}, fmt.Errorf("task %d updated_at: %w", row.ID, err)
	}
	var completed *time.Time
	if row.CompletedAt.Valid {
		c, err := parseTime(row.CompletedAt.String)
		if err != nil {
			return task.Task{}, fmt.Errorf("task %d completed_at: %w", row.ID, err)
		}
		completed = &c
	}
	return task.Task{
		ID:          row.ID,
		Title:       row.Title,
		BodyMD:      row.BodyMd,
		State:       task.State(row.State),
		ParentID:    nullInt64(row.ParentID),
		ProjectID:   row.ProjectID,
		Priority:    nullInt64(row.Priority),
		Due:         nullString(row.Due),
		SnoozeUntil: nullString(row.SnoozeUntil),
		CreatedAt:   created,
		UpdatedAt:   updated,
		CompletedAt: completed,
	}, nil
}

func logToDomain(row gen.LogEntry) (task.LogEntry, error) {
	created, err := parseTime(row.CreatedAt)
	if err != nil {
		return task.LogEntry{}, fmt.Errorf("log entry %d created_at: %w", row.ID, err)
	}
	return task.LogEntry{
		ID:        row.ID,
		TaskID:    nullInt64(row.TaskID),
		Body:      row.Body,
		CreatedAt: created,
	}, nil
}

func sessionToDomain(row gen.AgentSession) (task.Session, error) {
	started, err := parseTime(row.StartedAt)
	if err != nil {
		return task.Session{}, fmt.Errorf("session %d started_at: %w", row.ID, err)
	}
	lastActive, err := parseTime(row.LastActiveAt)
	if err != nil {
		return task.Session{}, fmt.Errorf("session %d last_active_at: %w", row.ID, err)
	}
	// status_updated_at is nullable and, unlike started_at/last_active_at,
	// an unparseable value is not worth failing the whole read over: it's
	// a display timestamp on a cached observation, so a bad one degrades
	// to the zero time (see task.SessionStatus).
	var statusUpdated time.Time
	if row.StatusUpdatedAt.Valid {
		statusUpdated, _ = parseTime(row.StatusUpdatedAt.String)
	}
	return task.Session{
		ID:              row.ID,
		TaskID:          row.TaskID,
		ExternalID:      row.ExternalID,
		Cwd:             row.Cwd,
		Label:           row.Label,
		TmuxSession:     row.TmuxSession,
		NeedsRecap:      row.NeedsRecap != 0,
		Status:          task.SessionStatus(row.Status),
		StatusUpdatedAt: statusUpdated,
		StartedAt:       started,
		LastActiveAt:    lastActive,
	}, nil
}

func eventToDomain(row gen.TaskEvent) (task.Event, error) {
	created, err := parseTime(row.CreatedAt)
	if err != nil {
		return task.Event{}, fmt.Errorf("event %d created_at: %w", row.ID, err)
	}
	return task.Event{
		ID:        row.ID,
		TaskID:    row.TaskID,
		TaskTitle: row.TaskTitle,
		Kind:      task.EventKind(row.Kind),
		Old:       nullString(row.OldValue),
		New:       nullString(row.NewValue),
		CreatedAt: created,
	}, nil
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(sqliteTimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing %q: %w", s, err)
	}
	return t, nil
}

func toNullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

func nullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func toNullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func nullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
