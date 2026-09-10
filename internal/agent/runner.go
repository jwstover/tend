package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// runnerSessionPrefix namespaces the tmux sessions hosting workflow
// runners, apart from the "tend-<uuid>" sessions wrapping interactive
// claude sessions, so the two are told apart at a glance in
// `tmux -L tend ls`.
const runnerSessionPrefix = "tend-wf-"

// RunnerSessionName is the tmux session hosting a workflow run's runner
// (`tend workflow run <run-id>`), keyed by run id: the run exists before
// its runner does, and the name has to be derivable from the row alone
// so `tend workflow resume` can ask tmux whether the runner is alive.
func RunnerSessionName(runID int64) string {
	return runnerSessionPrefix + strconv.FormatInt(runID, 10)
}

// RunnerCmd builds the command that drives one workflow run: the hidden
// `tend workflow run <run-id> --db <path>`, using this very executable
// rather than whatever `tend` is on $PATH, so a run started from a
// development build is driven by that build. takeover adds --takeover,
// which lets the runner re-enter a run whose previous runner died
// without releasing it (see the runner package). --db is explicit for
// the same reason it is in the hook settings: the runner inherits tmux's
// environment, not the TUI's, so a --db or TEND_DB override would
// otherwise be lost.
//
// Like the rest of the package this only builds the command; see
// StartDetached for hosting it in tmux.
func RunnerCmd(runID int64, dbPath string, takeover bool) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating the tend binary: %w", err)
	}
	args := []string{"workflow", "run", strconv.FormatInt(runID, 10), "--db", dbPath}
	if takeover {
		args = append(args, "--takeover")
	}
	return exec.Command(self, args...), nil
}
