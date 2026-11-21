package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/launchd"
	"github.com/jwstover/tend/internal/systemd"
)

var errUnsupportedOS = errors.New("tend schedule install supports macOS (launchd) and Linux (systemd)")

// Seams so tests never touch launchctl or the real GOOS.
// executablePath is the seam for the running binary's path.
var executablePath = os.Executable

var (
	goos             = runtime.GOOS
	launchdInstall   = launchd.Install
	launchdUninstall = launchd.Uninstall
	systemdInstall   = systemd.Install
	systemdUninstall = systemd.Uninstall
)

// installConfig resolves what the agent bakes in: the installed binary, an
// absolute --db, the shell's PATH and a log beside the database.
// The launchd and systemd configs are separate types with the same fields.
func installConfig(dbPath string) (launchd.Config, error) {
	exe, err := executablePath()
	if err != nil {
		return launchd.Config{}, fmt.Errorf("finding the tend binary: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return launchd.Config{}, fmt.Errorf("resolving the tend binary: %w", err)
	}
	if strings.Contains(exe, "go-build") {
		return launchd.Config{}, errors.New("install from an installed binary (`make install`)")
	}
	db, err := filepath.Abs(dbPath)
	if err != nil {
		return launchd.Config{}, fmt.Errorf("resolving the database path: %w", err)
	}
	return launchd.Config{
		Executable: exe,
		DBPath:     db,
		Path:       os.Getenv("PATH"),
		LogPath:    filepath.Join(filepath.Dir(db), "schedule.log"),
	}, nil
}

func newScheduleInstallCmd(dbPath func() string) *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the launchd agent (macOS) or systemd timer (Linux) that runs `tend schedule tick` every minute",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if goos != "darwin" && goos != "linux" {
				return errUnsupportedOS
			}
			cfg, err := installConfig(dbPath())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if goos == "linux" {
				sc := systemd.Config(cfg)
				if printOnly {
					fmt.Fprintf(out, "# %s.service\n%s\n# %s.timer\n%s", systemd.Unit, systemd.ServiceUnit(sc), systemd.Unit, systemd.TimerUnit())
					return nil
				}
				dir, err := systemdInstall(cmd.Context(), sc)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "installed %s/%s.{service,timer}\nlog: %s\ntend schedule tick will run every minute\n", dir, systemd.Unit, cfg.LogPath)
				return nil
			}
			if printOnly {
				_, err := out.Write(launchd.Plist(cfg))
				return err
			}
			path, err := launchdInstall(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "installed %s\nlog: %s\ntend schedule tick will run every minute\n", path, cfg.LogPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the plist (macOS) or units (Linux) and install nothing")
	return cmd
}

func newScheduleUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the launchd agent or systemd timer installed by `tend schedule install`",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch goos {
			case "darwin":
				if err := launchdUninstall(cmd.Context()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "uninstalled the tend schedule launchd agent")
			case "linux":
				if err := systemdUninstall(cmd.Context()); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "uninstalled the tend schedule systemd timer")
			default:
				return errUnsupportedOS
			}
			return nil
		},
	}
}
