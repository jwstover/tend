package workflow

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/template"
)

// ErrInvalidPrompt is returned when a step's prompt_md does not parse or
// render as a Go text/template. The wrapping error carries the template
// engine's message, which names the offending variable and position, so
// the TUI's validate action and the launch path can show it as-is.
var ErrInvalidPrompt = errors.New("invalid prompt template")

// PromptTask is the slice of a task a prompt template can see. It is its
// own type rather than task.Task so the template vocabulary ({{.Task.Body}})
// stays stable even if the task struct grows or renames fields, and so this
// package keeps depending on nothing.
type PromptTask struct {
	ID    int64
	Title string
	Body  string
}

// PromptSubtask is one direct sub-task of the run's task as a prompt
// template sees it. IsBlocked says whether any task it waits on is still
// open; DependsOn lists every task it waits on, open or done, so a prompt
// can spell out the ready set ("sub-tasks not done and not blocked")
// rather than the agent having to call list_subtasks to work it out.
type PromptSubtask struct {
	ID        int64
	Title     string
	State     string
	IsBlocked bool
	DependsOn []int64
}

// PromptData is the variable set a step prompt is rendered against. Every
// exported field is a template variable; anything else is a render error.
//
//	{{.Task.Title}} {{.Task.Body}} {{.Task.ID}}
//	{{.Cwd}}
//	{{.Input}}      previous step's deliverable, "" for the first step
//	{{.Feedback}}   deliverable of the step that routed here on a
//	                reject-style outcome, "" otherwise
//	{{.Iteration}}  1-based count of this step within the run
//	{{.Outcomes}}   allowed outcomes for this step, so a prompt can tell
//	                the agent what finish_step accepts
//	{{.Subtasks}}   the task's direct sub-tasks, oldest first, each with
//	                ID, Title, State, IsBlocked and DependsOn; empty when
//	                the task has none, so {{range .Subtasks}} renders nothing
type PromptData struct {
	Task      PromptTask
	Cwd       string
	Input     string
	Feedback  string
	Iteration int64
	Outcomes  []string
	Subtasks  []PromptSubtask
}

// RenderPrompt renders a step's prompt_md against data. Unknown variables
// are an error, never silently empty: a typo in a prompt surfaces the
// first time it is rendered rather than as a confused agent.
func RenderPrompt(promptMD string, data PromptData) (string, error) {
	tmpl, err := parsePrompt(promptMD)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPrompt, err)
	}
	return sb.String(), nil
}

// ValidatePrompt checks a prompt at authoring time, before any run exists
// to render it against. It parses the template and executes it twice, once
// against zero data and once against fully populated sample data, so both
// arms of the usual {{if .Feedback}}...{{else}}...{{end}} pattern are
// exercised and an unknown variable inside either is caught.
func ValidatePrompt(promptMD string) error {
	tmpl, err := parsePrompt(promptMD)
	if err != nil {
		return err
	}
	for _, data := range []PromptData{{}, samplePromptData} {
		if err := tmpl.Execute(io.Discard, data); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPrompt, err)
		}
	}
	return nil
}

// samplePromptData has every field non-zero so validation walks the
// truthy branch of any conditional. Its Subtasks mix a ready one with a
// blocked one so a {{range .Subtasks}} body that reads IsBlocked or
// DependsOn is exercised both ways; the zero-value PromptData that
// validation also runs against covers the empty list.
var samplePromptData = PromptData{
	Task:      PromptTask{ID: 1, Title: "sample task", Body: "sample body"},
	Cwd:       "/tmp/sample",
	Input:     "sample input",
	Feedback:  "sample feedback",
	Iteration: 1,
	Outcomes:  []string{OutcomeDone},
	Subtasks: []PromptSubtask{
		{ID: 2, Title: "sample sub-task", State: "todo"},
		{ID: 3, Title: "sample blocked sub-task", State: "todo", IsBlocked: true, DependsOn: []int64{2}},
	},
}

func parsePrompt(promptMD string) (*template.Template, error) {
	tmpl, err := template.New("prompt").Option("missingkey=error").Parse(promptMD)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPrompt, err)
	}
	return tmpl, nil
}
