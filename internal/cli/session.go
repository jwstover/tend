package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/task"
)

// preparedJSON is what `tend session prepare` prints: the session id to
// start claude with and the files to start it with (agent.SessionFiles).
type preparedJSON struct {
	Session string `json:"session"`
	agent.SessionFiles
}

// newSessionCmd is the `tend session` group: the half of a task-bound
// Claude session that another program starts and hosts (devhub, in its
// own terminals). tend records the session and writes its MCP config,
// hook settings and brief; the host runs claude with them, so the session
// gets the task's tools and reports its status here like one tend
// launched. tend never attaches to, resumes or kills a hosted session.
func newSessionCmd(open func(context.Context) (TaskStore, error), dbPath func() string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Prepare and discard Claude sessions another program hosts",
	}
	cmd.AddCommand(newSessionPrepareCmd(open, dbPath), newSessionDiscardCmd(open))
	return cmd
}

func newSessionPrepareCmd(open func(context.Context) (TaskStore, error), dbPath func() string) *cobra.Command {
	var cwd, dir, host, resume string
	cmd := &cobra.Command{
		Use:   "prepare <task-id>",
		Short: "Record a hosted session on a task and write the files it starts with",
		Long: "Record a new Claude session on a task, hosted by --host, and write " +
			"its MCP config, hook settings and task brief into <dir>/<session>. " +
			"Prints {session, mcp_config, settings, system_prompt_file} as JSON: " +
			"start claude with --session-id and those files. With --resume, write " +
			"fresh files for that hosted session instead (the brief as the task " +
			"stands now) and record no new one. A host that cannot start the " +
			"session takes the row back with `tend session discard`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			if host == "" || dir == "" {
				return fmt.Errorf("--host and --dir are required")
			}
			tendPath, err := tendBinary()
			if err != nil {
				return err
			}
			return withTaskStore(cmd, open, func(ctx context.Context, s TaskStore) error {
				out, err := prepare(ctx, s, prepareArgs{
					taskID: id, cwd: cwd, dir: dir, host: host, resume: resume,
					tendPath: tendPath, dbPath: dbPath(),
				})
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), out)
			})
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "directory the session runs in (required for a new session)")
	cmd.Flags().StringVar(&dir, "dir", "", "directory to write the session's files under")
	cmd.Flags().StringVar(&host, "host", "", "the program hosting the session's terminal, e.g. devhub")
	cmd.Flags().StringVar(&resume, "resume", "", "a hosted session of this task to write fresh files for")
	return cmd
}

type prepareArgs struct {
	taskID                 int64
	cwd, dir, host, resume string
	tendPath, dbPath       string
}

func prepare(ctx context.Context, s TaskStore, in prepareArgs) (preparedJSON, error) {
	b, err := s.SessionBrief(ctx, in.taskID)
	if err != nil {
		return preparedJSON{}, err
	}
	brief := task.SessionSystemPrompt(b)
	// The hooks and MCP server run from claude's directory, not ours.
	if in.dbPath, err = filepath.Abs(in.dbPath); err != nil {
		return preparedJSON{}, err
	}
	if in.resume != "" {
		sess, err := s.SessionByExternalID(ctx, in.resume)
		if err != nil {
			return preparedJSON{}, err
		}
		if sess.TaskID != in.taskID || sess.Host == "" {
			return preparedJSON{}, fmt.Errorf("session %s is not a hosted session of task %d", in.resume, in.taskID)
		}
		files, err := agent.WriteSessionFiles(filepath.Join(in.dir, sess.ExternalID), in.tendPath, in.taskID, in.dbPath, brief)
		if err != nil {
			return preparedJSON{}, err
		}
		if err := s.TouchSession(ctx, sess.ID); err != nil {
			return preparedJSON{}, err
		}
		return preparedJSON{Session: sess.ExternalID, SessionFiles: files}, nil
	}
	if in.cwd == "" {
		return preparedJSON{}, fmt.Errorf("--cwd is required for a new session")
	}
	id, err := agent.NewSessionID()
	if err != nil {
		return preparedJSON{}, err
	}
	files, err := agent.WriteSessionFiles(filepath.Join(in.dir, id), in.tendPath, in.taskID, in.dbPath, brief)
	if err != nil {
		return preparedJSON{}, err
	}
	if _, err := s.CreateHostedSession(ctx, in.taskID, id, in.cwd, b.Task.Title, in.host); err != nil {
		os.RemoveAll(filepath.Join(in.dir, id))
		return preparedJSON{}, err
	}
	return preparedJSON{Session: id, SessionFiles: files}, nil
}

func newSessionDiscardCmd(open func(context.Context) (TaskStore, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "discard <session>",
		Short: "Take back a hosted session its host could not start",
		Long: "Remove the row `tend session prepare` wrote, when the host could not " +
			"start the session. Only a hosted session nothing has been heard from " +
			"yet is removed. Prints {discarded: bool} as JSON.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTaskStore(cmd, open, func(ctx context.Context, s TaskStore) error {
				ok, err := s.DeleteHostedStartingSession(ctx, args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), map[string]bool{"discarded": ok})
			})
		},
	}
}

// tendBinary is the tend the session's MCP server and hooks run: the one
// on $PATH, as WriteMCPConfig uses, else this process.
func tendBinary() (string, error) {
	if p, err := exec.LookPath("tend"); err == nil {
		return filepath.Abs(p)
	}
	return os.Executable()
}
