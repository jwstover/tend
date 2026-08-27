package cli

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/task"
)

func newProjectsCmd(open func(context.Context) (Store, error)) *cobra.Command {
	root := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project", "proj"},
		Short:   "List and manage projects",
		Long: "Projects group tasks; every task belongs to exactly one. With no " +
			"subcommand, lists them with their live task counts.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return listProjects(cmd, open) },
	}
	root.AddCommand(
		&cobra.Command{
			Use:   "ls",
			Short: "List projects with their live task counts",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return listProjects(cmd, open) },
		},
		&cobra.Command{
			Use:   "add <name>",
			Short: "Create a project",
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return withStore(cmd, open, func(ctx context.Context, s Store) error {
					p, err := s.CreateProject(ctx, joinArgs(args))
					if err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "created project %s\n", p.Name)
					return nil
				})
			},
		},
		&cobra.Command{
			Use:   "rename <name> <new-name>",
			Short: "Rename a project",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return withStore(cmd, open, func(ctx context.Context, s Store) error {
					p, err := resolveProject(ctx, s, args[0])
					if err != nil {
						return err
					}
					if err := s.RenameProject(ctx, p.ID, args[1]); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "renamed %s to %s\n", p.Name, args[1])
					return nil
				})
			},
		},
		&cobra.Command{
			Use:     "rm <name>",
			Aliases: []string{"delete"},
			Short:   "Delete a project; its tasks move to Unsorted",
			Long: "Delete a project. Its tasks are not deleted -- they move to the " +
				"default project, because a project is a grouping and dropping one " +
				"must never drop work.",
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return withStore(cmd, open, func(ctx context.Context, s Store) error {
					p, err := resolveProject(ctx, s, joinArgs(args))
					if err != nil {
						return err
					}
					if err := s.DeleteProject(ctx, p.ID); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(),
						"deleted %s; its tasks moved to Unsorted\n", p.Name)
					return nil
				})
			},
		},
		newArchiveCmd(open, "archive", "Hide a project from the projects column", true),
		newArchiveCmd(open, "unarchive", "Restore an archived project", false),
	)
	return root
}

func newArchiveCmd(open func(context.Context) (Store, error), use, short string, archived bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <name>",
		Short: short,
		Long: short + ". Archiving leaves a project's tasks alone: they keep their " +
			"project and reappear if it is unarchived.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(cmd, open, func(ctx context.Context, s Store) error {
				p, err := resolveProject(ctx, s, joinArgs(args))
				if err != nil {
					return err
				}
				if err := s.SetProjectArchived(ctx, p.ID, archived); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%sd %s\n", use, p.Name)
				return nil
			})
		},
	}
}

func listProjects(cmd *cobra.Command, open func(context.Context) (Store, error)) error {
	return withStore(cmd, open, func(ctx context.Context, s Store) error {
		projects, err := s.ListProjects(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		for _, p := range projects {
			suffix := ""
			if p.Archived() {
				suffix = "  (archived)"
			}
			fmt.Fprintf(w, "%d\t%s\t%d%s\n", p.ID, p.Name, p.LiveCount, suffix)
		}
		return w.Flush()
	})
}

// withStore opens the store, runs fn, and closes it. Every project
// subcommand has the same shape, and repeating it eight times buried the
// one line that differs.
func withStore(cmd *cobra.Command, open func(context.Context) (Store, error),
	fn func(context.Context, Store) error) error {
	ctx := cmd.Context()
	s, err := open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(ctx, s)
}

// resolveProject looks a project up by name and turns a miss into a
// pointed error. Names are matched case-insensitively but never created
// on the fly: a typo should say so, not quietly spawn a project.
func resolveProject(ctx context.Context, s Store, name string) (task.Project, error) {
	p, err := s.ProjectByName(ctx, name)
	if errors.Is(err, task.ErrProjectNotFound) {
		return task.Project{}, fmt.Errorf(
			"no project named %q (create it with `tend projects add %s`)", name, name)
	}
	return p, err
}

// joinArgs lets a project name be typed unquoted: `tend projects add my
// project` is the same as `tend projects add "my project"`, matching how
// `tend add` treats a title.
func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
