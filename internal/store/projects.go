package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
)

// settingActiveProject remembers which project the TUI's projects column
// was last on, so reopening the TUI lands where you left off.
//
// It is deliberately NOT a capture target. Capture with no project named
// goes to the default project, so the shell has no hidden state steering
// it; this key is read and written by the TUI alone.
const settingActiveProject = "active_project_id"

// inTx runs fn inside a transaction and commits, rolling back on any
// error. Every multi-statement invariant in this package goes through it:
// project_id carries no foreign key (see migration 00007), so the writes
// that stand in for one have to be atomic themselves.
func (s *Store) inTx(ctx context.Context, fn func(q *gen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	// Best-effort: after a successful Commit this is a no-op, and on the
	// error path the caller is already getting the real failure.
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// projectFilter converts a nil-able project id into the value sqlc
// generates for a nullable named argument. sqlc.narg on SQLite emits an
// untyped interface{} rather than sql.NullInt64, so nil means "every
// project" and a bare int64 scopes the query.
func projectFilter(id *int64) interface{} {
	if id == nil {
		return nil
	}
	return *id
}

// CreateProject adds a project. Names are unique case-insensitively; a
// collision surfaces as the driver's constraint error.
func (s *Store) CreateProject(ctx context.Context, name string) (task.Project, error) {
	n, err := task.NormalizeProjectName(name)
	if err != nil {
		return task.Project{}, err
	}
	row, err := s.q.CreateProject(ctx, n)
	if err != nil {
		return task.Project{}, fmt.Errorf("creating project %q: %w", n, err)
	}
	return projectToDomain(row, 0)
}

// GetProject loads one project by id.
func (s *Store) GetProject(ctx context.Context, id int64) (task.Project, error) {
	row, err := s.q.GetProject(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Project{}, fmt.Errorf("project %d: %w", id, task.ErrProjectNotFound)
	}
	if err != nil {
		return task.Project{}, fmt.Errorf("loading project %d: %w", id, err)
	}
	return projectToDomain(row, 0)
}

// ProjectByName resolves a project by name, case-insensitively. Callers
// that accept a name from the user (`tend add -p`) match on
// task.ErrProjectNotFound rather than creating one, so a typo can't
// silently spawn a project.
func (s *Store) ProjectByName(ctx context.Context, name string) (task.Project, error) {
	n, err := task.NormalizeProjectName(name)
	if err != nil {
		return task.Project{}, err
	}
	row, err := s.q.GetProjectByName(ctx, n)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Project{}, fmt.Errorf("project %q: %w", n, task.ErrProjectNotFound)
	}
	if err != nil {
		return task.Project{}, fmt.Errorf("loading project %q: %w", n, err)
	}
	return projectToDomain(row, 0)
}

// ListProjects returns every project with its live top-level task count,
// archived ones included — the projects column filters those out itself,
// and `tend projects` wants to show them.
func (s *Store) ListProjects(ctx context.Context) ([]task.Project, error) {
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	out := make([]task.Project, 0, len(rows))
	for _, r := range rows {
		p, err := projectToDomain(gen.Project{
			ID:         r.ID,
			Name:       r.Name,
			SortOrder:  r.SortOrder,
			ArchivedAt: r.ArchivedAt,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
		}, r.LiveCount)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// RenameProject changes a project's name.
func (s *Store) RenameProject(ctx context.Context, id int64, name string) error {
	n, err := task.NormalizeProjectName(name)
	if err != nil {
		return err
	}
	if err := s.q.RenameProject(ctx, gen.RenameProjectParams{Name: n, ID: id}); err != nil {
		return fmt.Errorf("renaming project %d: %w", id, err)
	}
	return nil
}

// SetProjectArchived hides or restores a project in the projects column.
// Archiving leaves its tasks alone: they keep their project_id and
// reappear if it's unarchived.
func (s *Store) SetProjectArchived(ctx context.Context, id int64, archived bool) error {
	var at sql.NullString
	if archived {
		at = sql.NullString{String: time.Now().UTC().Format(sqliteTimeLayout), Valid: true}
	}
	if err := s.q.SetProjectArchived(ctx, gen.SetProjectArchivedParams{ArchivedAt: at, ID: id}); err != nil {
		return fmt.Errorf("archiving project %d: %w", id, err)
	}
	return nil
}

// DeleteProject removes a project after reassigning its tasks to the
// default project, both in one transaction. This is the orphan prevention
// that a foreign key would otherwise provide (see migration 00007) — a
// task must always point at a project that exists.
//
// The default project itself is refused: it is the reassignment target and
// the capture fallback, so deleting it would strand every path that leans
// on it. An active_project_id pointing at the deleted row needs no cleanup
// — ActiveProjectID falls back when the row is missing.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	if id == task.DefaultProjectID {
		return task.ErrProtectedProject
	}
	return s.inTx(ctx, func(q *gen.Queries) error {
		if err := q.ReassignProjectTasks(ctx, gen.ReassignProjectTasksParams{
			ToProjectID:   task.DefaultProjectID,
			FromProjectID: id,
		}); err != nil {
			return fmt.Errorf("reassigning tasks out of project %d: %w", id, err)
		}
		if err := q.DeleteProject(ctx, id); err != nil {
			return fmt.Errorf("deleting project %d: %w", id, err)
		}
		return nil
	})
}

// ActiveProjectID returns the project the TUI should reopen on, falling
// back to the default project whenever the stored value is missing,
// unparseable or points at a project that has since been deleted. It
// never fails on a bad value -- a corrupt UI preference should not stop
// the TUI from starting.
func (s *Store) ActiveProjectID(ctx context.Context) (int64, error) {
	v, err := s.q.GetSetting(ctx, settingActiveProject)
	if errors.Is(err, sql.ErrNoRows) {
		return task.DefaultProjectID, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading active project: %w", err)
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return task.DefaultProjectID, nil
	}
	if _, err := s.q.GetProject(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return task.DefaultProjectID, nil
		}
		return 0, fmt.Errorf("resolving active project %d: %w", id, err)
	}
	return id, nil
}

// SetActiveProject records the project the TUI should reopen on.
func (s *Store) SetActiveProject(ctx context.Context, id int64) error {
	if err := s.q.SetSetting(ctx, gen.SetSettingParams{
		Key:   settingActiveProject,
		Value: strconv.FormatInt(id, 10),
	}); err != nil {
		return fmt.Errorf("setting active project to %d: %w", id, err)
	}
	return nil
}

func projectToDomain(row gen.Project, liveCount int64) (task.Project, error) {
	created, err := parseTime(row.CreatedAt)
	if err != nil {
		return task.Project{}, fmt.Errorf("project %d created_at: %w", row.ID, err)
	}
	updated, err := parseTime(row.UpdatedAt)
	if err != nil {
		return task.Project{}, fmt.Errorf("project %d updated_at: %w", row.ID, err)
	}
	var archived *time.Time
	if row.ArchivedAt.Valid {
		a, err := parseTime(row.ArchivedAt.String)
		if err != nil {
			return task.Project{}, fmt.Errorf("project %d archived_at: %w", row.ID, err)
		}
		archived = &a
	}
	return task.Project{
		ID:         row.ID,
		Name:       row.Name,
		SortOrder:  row.SortOrder,
		ArchivedAt: archived,
		CreatedAt:  created,
		UpdatedAt:  updated,
		LiveCount:  liveCount,
	}, nil
}
