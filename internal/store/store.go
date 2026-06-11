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

// AddTask captures a task: a bare title, everything else defaulted by the
// schema (state inbox, empty body).
func (s *Store) AddTask(ctx context.Context, title string) (task.Task, error) {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return task.Task{}, err
	}
	row, err := s.q.CreateTask(ctx, t)
	if err != nil {
		return task.Task{}, fmt.Errorf("inserting task: %w", err)
	}
	return toDomain(row)
}

// ListLive returns the live view: non-terminal, non-hidden states, and
// not snoozed into the future.
func (s *Store) ListLive(ctx context.Context) ([]task.Task, error) {
	rows, err := s.q.ListLiveTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing live tasks: %w", err)
	}
	return toDomainSlice(rows)
}

// AddChild captures a sub-task under an existing task.
func (s *Store) AddChild(ctx context.Context, parentID int64, title string) (task.Task, error) {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return task.Task{}, err
	}
	row, err := s.q.CreateChildTask(ctx, gen.CreateChildTaskParams{
		Title:    t,
		ParentID: sql.NullInt64{Int64: parentID, Valid: true},
	})
	if err != nil {
		return task.Task{}, fmt.Errorf("inserting sub-task of %d: %w", parentID, err)
	}
	return toDomain(row)
}

// ListInbox returns every task still in the inbox state, oldest first.
// This feeds the triage view.
func (s *Store) ListInbox(ctx context.Context) ([]task.Task, error) {
	rows, err := s.q.ListInboxTasks(ctx)
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

// CountInbox returns the number of tasks awaiting triage.
func (s *Store) CountInbox(ctx context.Context) (int64, error) {
	n, err := s.q.CountInboxTasks(ctx)
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

// SetProject assigns a project to a task; nil clears it.
func (s *Store) SetProject(ctx context.Context, id int64, project *string) error {
	err := s.q.SetTaskProject(ctx, gen.SetTaskProjectParams{
		Project: toNullString(project),
		ID:      id,
	})
	if err != nil {
		return fmt.Errorf("setting task %d project: %w", id, err)
	}
	return nil
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

// SetBody replaces a task's markdown body.
func (s *Store) SetBody(ctx context.Context, id int64, body string) error {
	if err := s.q.SetTaskBody(ctx, gen.SetTaskBodyParams{BodyMd: body, ID: id}); err != nil {
		return fmt.Errorf("setting task %d body: %w", id, err)
	}
	return nil
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
		Project:     nullString(row.Project),
		Priority:    nullInt64(row.Priority),
		Due:         nullString(row.Due),
		SnoozeUntil: nullString(row.SnoozeUntil),
		CreatedAt:   created,
		UpdatedAt:   updated,
		CompletedAt: completed,
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
