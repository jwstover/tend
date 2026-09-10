package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/workflow"
)

// TestRunRealClaudeHandsOffReview is the acceptance check for tend task
// #197 against the installed CLI: the implement / review / ship workflow
// whose review step, on haiku, once "approved" in prose and never called
// finish_step, so the run ended silently with the verdict lost. The
// review prompt here deliberately says nothing about finish_step -- the
// runner-injected system prompt has to carry the contract, or the nudge
// has to recover it. Either way the review must end in an approve
// outcome that routes to ship.
//
// It builds the current tend binary onto $PATH (ClaudeExec finds `tend`
// there for the MCP config), costs a few haiku calls, and is skipped
// under -short and wherever claude is not installed -- CI included.
func TestRunRealClaudeHandsOffReview(t *testing.T) {
	if testing.Short() {
		t.Skip("real claude run skipped in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	binDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "tend"), "github.com/jwstover/tend/cmd/tend")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building tend: %v\n%s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// TEND_E2E_DATA keeps the run's step logs somewhere readable after
	// the test, for looking at what the model actually did.
	dataDir := os.Getenv("TEND_E2E_DATA")
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	t.Setenv("XDG_DATA_HOME", dataDir)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	dbPath := filepath.Join(t.TempDir(), "tend.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	// claude keys its transcript directory by the resolved cwd, and on
	// macOS t.TempDir() lives under the /var -> /private/var symlink.
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tk, err := s.AddTask(ctx, "e2e: hand off a review verdict")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := s.CreateWorkflow(ctx, "implement / review / ship", "")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name, prompt string) workflow.Step {
		st, err := s.AddStep(ctx, wf.ID, name, workflow.StepAgent)
		if err != nil {
			t.Fatalf("AddStep(%s): %v", name, err)
		}
		if err := s.SetStepPrompt(ctx, st.ID, prompt); err != nil {
			t.Fatal(err)
		}
		if err := s.SetStepModel(ctx, st.ID, "haiku"); err != nil {
			t.Fatal(err)
		}
		// MCP tool calls need approval a -p run cannot give; bypass is
		// safe here because the prompts use no other tools and cwd is empty.
		if err := s.SetStepPermissionMode(ctx, st.ID, "bypassPermissions"); err != nil {
			t.Fatal(err)
		}
		return st
	}
	implement := mk("implement", "Pretend you implemented the task {{.Task.Title}}. Your deliverable is the one line: "+
		"\"Implemented: added the missing hand-off\". Do not read or write any files.")
	review := mk("review", "You are reviewing this implementation: {{.Input}}\n\n"+
		"The change is acceptable. Give your verdict: it is approved. Do not read or write any files.")
	ship := mk("ship", "Reply with exactly the word SHIPPED. Do not use any tools other than tend's.")
	if _, err := s.SetEdge(ctx, implement.ID, "done", review.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEdge(ctx, review.ID, "approve", ship.ID, nil); err != nil {
		t.Fatal(err)
	}
	two := int64(2)
	if _, err := s.SetEdge(ctx, review.ID, "reject", implement.ID, &two); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, wf.ID, tk.ID, cwd)
	if err != nil {
		t.Fatal(err)
	}

	var log strings.Builder
	r := &Runner{Store: s, Exec: ClaudeExec{DBPath: dbPath}, Poll: 500 * time.Millisecond, Log: &log}
	err = r.Run(ctx, run.ID, false)
	t.Logf("runner log:\n%s", log.String())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != workflow.RunDone {
		t.Fatalf("run = %+v, want done", got)
	}
	srs, err := s.ListStepRunsForRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(srs) != 3 {
		t.Fatalf("step runs = %d, want implement, review, ship", len(srs))
	}
	rev := srs[1]
	if rev.StepID != review.ID || rev.Outcome != "approve" {
		t.Errorf("review step run = %+v, want outcome approve handed off through finish_step", rev)
	}
	if !strings.Contains(rev.SystemPrompt, workflow.FinishStepTool) {
		t.Errorf("review system prompt = %q, want the injected hand-off block", rev.SystemPrompt)
	}
	if srs[2].StepID != ship.ID || srs[2].Input == "" {
		t.Errorf("ship step run = %+v, want it fed the review's deliverable", srs[2])
	}
	// Both attempts, if there were two, went to one log file.
	if res, ok := loggedResult(rev.LogPath); !ok || res.SessionID != rev.SessionExternalID {
		t.Errorf("review log %s: result %+v, want the review session's own result", rev.LogPath, res)
	}
	if strings.Contains(log.String(), "asked once to hand off") {
		t.Log("review step needed the nudge to hand off")
	} else {
		t.Log("review step handed off on its own under the injected system prompt")
	}
}
