package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/task"
)

// TaskStore is the slice of the persistence layer behind the scriptable
// task commands (`show`, `state`, `append`, `session`): one task's whole
// record, the two edits a front end makes most, and the rows of sessions
// another program hosts.
type TaskStore interface {
	SessionBrief(ctx context.Context, taskID int64) (task.SessionBrief, error)
	ListSessionsForTask(ctx context.Context, taskID int64) ([]task.Session, error)
	TaskBranches(ctx context.Context, taskID int64) ([]task.BranchLink, error)
	SetState(ctx context.Context, id int64, st task.State) error
	AppendBody(ctx context.Context, id int64, text string) error
	CreateHostedSession(ctx context.Context, taskID int64, externalID, cwd, label, host string) (task.Session, error)
	SessionByExternalID(ctx context.Context, externalID string) (task.Session, error)
	DeleteHostedStartingSession(ctx context.Context, externalID string) (bool, error)
	TouchSession(ctx context.Context, id int64) error
	Close() error
}

// TaskStoreFactory opens a TaskStore at the given database path.
type TaskStoreFactory func(ctx context.Context, dbPath string) (TaskStore, error)

// taskJSON is a task as the scriptable commands print it. Body is set
// only by `show`.
type taskJSON struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	ProjectID int64    `json:"project_id"`
	ParentID  *int64   `json:"parent_id,omitempty"`
	Tags      []string `json:"tags"`
	Priority  *int64   `json:"priority,omitempty"`
	Due       *string  `json:"due,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	Body      *string  `json:"body_md,omitempty"`
	// Sub-task progress over every sub-task, closed ones included, as the
	// TUI's N/M shows it; set by `ls`, 0/0 for a task without sub-tasks.
	ChildrenDone  int64 `json:"children_done"`
	ChildrenTotal int64 `json:"children_total"`
	// Sessions are the ids of its sessions that have not ended, most
	// recently active first; set by `ls`. A front end matches them to the
	// terminals it shows to say which task has a session running.
	Sessions []string `json:"sessions,omitempty"`
}

func toTaskJSON(t task.Task, tags []string) taskJSON {
	if tags == nil {
		tags = []string{}
	}
	return taskJSON{
		ID: t.ID, Title: t.Title, State: string(t.State), ProjectID: t.ProjectID,
		ParentID: t.ParentID, Tags: tags, Priority: t.Priority, Due: t.Due,
		UpdatedAt: stamp(t.UpdatedAt),
	}
}

func toTasksJSON(ts []task.Task) []taskJSON {
	out := make([]taskJSON, len(ts))
	for i, t := range ts {
		out[i] = toTaskJSON(t, nil)
	}
	return out
}

// projectJSON is a project as the scriptable commands print it. LiveCount
// is set by `ls` only: live top-level tasks, the number the TUI's projects
// column shows.
type projectJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd,omitempty"`
	Archived  bool   `json:"archived"`
	LiveCount *int64 `json:"live_count,omitempty"`
}

func toProjectJSON(p task.Project) projectJSON {
	return projectJSON{ID: p.ID, Name: p.Name, Cwd: p.Cwd, Archived: p.Archived()}
}

type sessionJSON struct {
	Session      string `json:"session"`
	Label        string `json:"label"`
	Status       string `json:"status"`
	Host         string `json:"host,omitempty"`
	Cwd          string `json:"cwd"`
	StartedAt    string `json:"started_at"`
	LastActiveAt string `json:"last_active_at"`
}

type branchJSON struct {
	RepoPath string `json:"repo_path"`
	Branch   string `json:"branch"`
}

type logJSON struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// showJSON is `tend show --json`: a task and everything around it.
// DefaultCwd is where a new session on it starts (task.DefaultCwd), "" when
// neither its sessions nor its project say.
type showJSON struct {
	Task       taskJSON      `json:"task"`
	Project    projectJSON   `json:"project"`
	DefaultCwd string        `json:"default_cwd"`
	Parent     *taskJSON     `json:"parent,omitempty"`
	Children   []taskJSON    `json:"children"`
	Blockers   []taskJSON    `json:"blockers"`
	Blocking   []taskJSON    `json:"blocking"`
	Sessions   []sessionJSON `json:"sessions"`
	Branches   []branchJSON  `json:"branches"`
	Log        []logJSON     `json:"log"`
}

// stamp renders a time as RFC 3339 in UTC, "" for the zero time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

func taskID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("not a task id: %q", arg)
	}
	return id, nil
}

// withTaskStore opens the store, runs f and closes it.
func withTaskStore(cmd *cobra.Command, open func(context.Context) (TaskStore, error), f func(context.Context, TaskStore) error) error {
	ctx := cmd.Context()
	s, err := open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	return f(ctx, s)
}

func newShowCmd(open func(context.Context) (TaskStore, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "show <task-id>",
		Short: "Print one task and everything around it as JSON",
		Long: "Print a task as JSON: its body, project, parent, sub-tasks, " +
			"dependencies both ways, Claude sessions, linked branches and log.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			return withTaskStore(cmd, open, func(ctx context.Context, s TaskStore) error {
				out, err := show(ctx, s, id)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), out)
			})
		},
	}
}

func show(ctx context.Context, s TaskStore, id int64) (showJSON, error) {
	b, err := s.SessionBrief(ctx, id)
	if err != nil {
		return showJSON{}, err
	}
	t := toTaskJSON(b.Task, b.Tags)
	t.Body = &b.Task.BodyMD
	sessions, err := s.ListSessionsForTask(ctx, id)
	if err != nil {
		return showJSON{}, err
	}
	out := showJSON{
		Task:       t,
		Project:    toProjectJSON(b.Project),
		DefaultCwd: task.DefaultCwd(sessions, b.Project),
		Children:   toTasksJSON(b.Children),
		Blockers:   toTasksJSON(b.Blockers),
		Blocking:   toTasksJSON(b.Blocking),
		Sessions:   []sessionJSON{},
		Branches:   []branchJSON{},
		Log:        []logJSON{},
	}
	if b.Parent != nil {
		p := toTaskJSON(*b.Parent, nil)
		out.Parent = &p
	}
	for _, se := range sessions {
		out.Sessions = append(out.Sessions, sessionJSON{
			Session: se.ExternalID, Label: se.Label, Status: string(se.Status), Host: se.Host,
			Cwd: se.Cwd, StartedAt: stamp(se.StartedAt), LastActiveAt: stamp(se.LastActiveAt),
		})
	}
	branches, err := s.TaskBranches(ctx, id)
	if err != nil {
		return showJSON{}, err
	}
	for _, bl := range branches {
		out.Branches = append(out.Branches, branchJSON{RepoPath: bl.RepoPath, Branch: bl.Branch})
	}
	for _, l := range b.Log {
		out.Log = append(out.Log, logJSON{Body: l.Body, CreatedAt: stamp(l.CreatedAt)})
	}
	return out, nil
}

func newStateCmd(open func(context.Context) (TaskStore, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "state <task-id> <state>",
		Short: "Move a task to a state, and print it as JSON",
		Long: "Move a task to inbox, todo, doing, review, blocked, done, canceled " +
			"or someday, recorded in its activity like a move in the TUI. Prints " +
			"the task as `tend show` does.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			st := task.State(args[1])
			if !st.Valid() {
				return fmt.Errorf("not a state: %q", args[1])
			}
			return withTaskStore(cmd, open, func(ctx context.Context, s TaskStore) error {
				if err := s.SetState(ctx, id, st); err != nil {
					return err
				}
				out, err := show(ctx, s, id)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), out)
			})
		},
	}
}

func newAppendCmd(open func(context.Context) (TaskStore, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "append <task-id> [text]",
		Short: "Append markdown to a task's body, and print it as JSON",
		Long: "Append markdown to the end of a task's body as a new paragraph, " +
			"from the argument or else stdin. Prints the task as `tend show` does.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			text := ""
			if len(args) == 2 {
				text = args[1]
			} else {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(b)
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("nothing to append")
			}
			return withTaskStore(cmd, open, func(ctx context.Context, s TaskStore) error {
				if err := s.AppendBody(ctx, id, text); err != nil {
					return err
				}
				out, err := show(ctx, s, id)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), out)
			})
		},
	}
}
