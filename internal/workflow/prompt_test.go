package workflow

import (
	"errors"
	"strings"
	"testing"
)

var fullData = PromptData{
	Task:      PromptTask{ID: 42, Title: "Fix the flaky test", Body: "It fails on CI only."},
	Cwd:       "/home/me/proj",
	Input:     "previous deliverable",
	Feedback:  "reviewer said no",
	Iteration: 3,
	Outcomes:  []string{"approve", "reject"},
	Subtasks: []PromptSubtask{
		{ID: 43, Title: "write the migration", State: "done"},
		{ID: 44, Title: "wire the store", State: "todo", DependsOn: []int64{43}},
		{ID: 45, Title: "expose over MCP", State: "todo", IsBlocked: true, DependsOn: []int64{44}},
	},
}

// readySetTpl is the Dispatch step's job in template form: the sub-tasks
// that are neither done nor waiting on an open one.
const readySetTpl = "{{range .Subtasks}}{{if and (ne .State \"done\") (not .IsBlocked)}}- #{{.ID}} {{.Title}}\n{{end}}{{end}}"

func TestRenderPrompt(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		data   PromptData
		want   string
		err    error
	}{
		{
			name:   "no variables (the POC case) passes through verbatim",
			prompt: "Fix the bug described in the task.\n\nRun the tests.",
			data:   PromptData{},
			want:   "Fix the bug described in the task.\n\nRun the tests.",
		},
		{
			name:   "empty prompt renders empty",
			prompt: "",
			data:   fullData,
			want:   "",
		},
		{
			name: "every variable",
			prompt: "Task #{{.Task.ID}}: {{.Task.Title}}\n{{.Task.Body}}\n" +
				"cwd={{.Cwd}} input={{.Input}} feedback={{.Feedback}} iteration={{.Iteration}}\n" +
				"outcomes: {{range $i, $o := .Outcomes}}{{if $i}}, {{end}}{{$o}}{{end}}",
			data: fullData,
			want: "Task #42: Fix the flaky test\nIt fails on CI only.\n" +
				"cwd=/home/me/proj input=previous deliverable feedback=reviewer said no iteration=3\n" +
				"outcomes: approve, reject",
		},
		{
			name:   "first step: empty input and feedback render as nothing, not <nil>",
			prompt: "[{{.Input}}][{{.Feedback}}]",
			data:   PromptData{Iteration: 1},
			want:   "[][]",
		},
		{
			name:   "conditional feedback block",
			prompt: "Do it.{{if .Feedback}} Address this feedback: {{.Feedback}}{{end}}",
			data:   PromptData{Feedback: "make it faster"},
			want:   "Do it. Address this feedback: make it faster",
		},
		{
			name:   "range over sub-tasks with every field",
			prompt: "{{range .Subtasks}}#{{.ID}} {{.Title}} [{{.State}}] blocked={{.IsBlocked}} deps={{.DependsOn}}\n{{end}}",
			data:   fullData,
			want: "#43 write the migration [done] blocked=false deps=[]\n" +
				"#44 wire the store [todo] blocked=false deps=[43]\n" +
				"#45 expose over MCP [todo] blocked=true deps=[44]\n",
		},
		{
			name:   "ready set: not done and not blocked",
			prompt: readySetTpl,
			data:   fullData,
			want:   "- #44 wire the store\n",
		},
		{
			name:   "no sub-tasks: range renders nothing, not an error",
			prompt: "sub-tasks:{{range .Subtasks}} {{.Title}}{{end}}",
			data:   PromptData{Task: PromptTask{ID: 1}},
			want:   "sub-tasks:",
		},
		{
			name:   "no sub-tasks: bare {{.Subtasks}} renders as an empty list",
			prompt: "{{.Subtasks}}",
			data:   PromptData{},
			want:   "[]",
		},
		{
			name:   "sub-task count with len",
			prompt: "{{len .Subtasks}} sub-tasks",
			data:   fullData,
			want:   "3 sub-tasks",
		},
		{
			name:   "unknown sub-task field is an error",
			prompt: "{{range .Subtasks}}{{.Body}}{{end}}",
			data:   fullData,
			err:    ErrInvalidPrompt,
		},
		{
			name:   "unknown top-level variable is an error",
			prompt: "Hello {{.Nope}}",
			data:   fullData,
			err:    ErrInvalidPrompt,
		},
		{
			name:   "unknown nested variable is an error",
			prompt: "{{.Task.BodyMD}}",
			data:   fullData,
			err:    ErrInvalidPrompt,
		},
		{
			name:   "syntax error is an error",
			prompt: "{{.Task.Title",
			data:   fullData,
			err:    ErrInvalidPrompt,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RenderPrompt(c.prompt, c.data)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRenderPromptErrorNamesTheVariable(t *testing.T) {
	_, err := RenderPrompt("{{.Task.Title}} {{.Tittle}}", fullData)
	if err == nil || !strings.Contains(err.Error(), "Tittle") {
		t.Errorf("error should name the unknown variable, got %v", err)
	}
}

func TestValidatePrompt(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		err    error
	}{
		{"plain text", "Just do the thing.", nil},
		{"every variable", "{{.Task.ID}}{{.Task.Title}}{{.Task.Body}}{{.Cwd}}{{.Input}}{{.Feedback}}{{.Iteration}}{{.Outcomes}}", nil},
		{"range over outcomes", "{{range .Outcomes}}- {{.}}\n{{end}}", nil},
		{"bare subtasks", "{{.Subtasks}}", nil},
		{"range over sub-tasks with every field", "{{range .Subtasks}}{{.ID}}{{.Title}}{{.State}}{{.IsBlocked}}{{.DependsOn}}{{end}}", nil},
		{"ready-set template", readySetTpl, nil},
		{"range over each sub-task's dependencies", "{{range .Subtasks}}{{range .DependsOn}}#{{.}} {{end}}{{end}}", nil},
		{"unknown sub-task field", "{{range .Subtasks}}{{.Body}}{{end}}", ErrInvalidPrompt},
		{"unknown field inside blocked branch", "{{range .Subtasks}}{{if .IsBlocked}}{{.Blockers}}{{end}}{{end}}", ErrInvalidPrompt},
		{"unknown field inside unblocked branch", "{{range .Subtasks}}{{if .IsBlocked}}ok{{else}}{{.Blockers}}{{end}}{{end}}", ErrInvalidPrompt},
		{"unknown variable", "{{.Cwdd}}", ErrInvalidPrompt},
		{"unknown variable inside truthy branch", "{{if .Feedback}}{{.Bogus}}{{end}}", ErrInvalidPrompt},
		{"unknown variable inside falsy branch", "{{if .Feedback}}ok{{else}}{{.Bogus}}{{end}}", ErrInvalidPrompt},
		{"unclosed action", "{{.Cwd", ErrInvalidPrompt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidatePrompt(c.prompt); !errors.Is(err, c.err) {
				t.Errorf("ValidatePrompt(%q) = %v, want %v", c.prompt, err, c.err)
			}
		})
	}
}
