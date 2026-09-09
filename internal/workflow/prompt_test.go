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
}

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
