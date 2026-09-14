defmodule Tend.Template.Parity.Go do
  @moduledoc """
  Renders parity cases through Go's own `text/template`.

  The driver below is written to a temporary directory and run with `go run`
  rather than checked in as a `.go` file: the Go tree in this repo is the one
  that ships, `go build ./...` covers all of it, and a test-only main package
  has no business in it. Nothing is written anywhere but the temp directory,
  and it is removed again afterwards.

  It renders exactly the way `internal/workflow/prompt.go`'s `RenderPrompt`
  does -- `template.New("prompt").Option("missingkey=error").Parse` then
  `Execute` -- against structs that mirror `PromptTask`, `PromptSubtask` and
  `PromptData` field for field. Those three declarations and the `datasets`
  map are the only thing this duplicates from the Go tree, and a drift in
  either shows up immediately as a parity failure.

  Cases go in as JSON lines (`{"name":..,"template":..,"data":..}`) and come
  back as JSON lines with `ok` plus either `output` or `error`, which keeps
  the byte comparison honest: no shell quoting anywhere near a template.

  The one thing JSON cannot carry is a rendered byte that is not valid UTF-8:
  Go's `encoding/json` replaces it with U+FFFD, so a template such as
  `{{"\\xff"}}` is reported as a mismatch that is really the transport's. A
  `prompt_md` is markdown a person typed and every one in the corpus is valid
  UTF-8, so this has never fired outside a lexer test; the fix, if it ever
  does, is to base64 the `output` field.

  ## Duplication to unify later

  Sub-task #265 landed a second Go build/run helper at `test/support/go.ex`,
  on a stack this one cannot see until the two merge. Neither is wrong and
  neither can import the other yet, so both stay; folding this module's
  `available?/0` and its `System.cmd("go", ...)` onto that one is a job for
  the sub-task that reconciles the stacks, not for this branch.
  """

  @go_mod """
  module tendparity

  go 1.22
  """

  @program ~S"""
  // Renders prompt templates through Go's text/template exactly as
  // internal/workflow/prompt.go's RenderPrompt does, for comparison against
  // the Elixir port. Written out and run by Tend.Template.Parity.Go.
  package main

  import (
  	"bufio"
  	"encoding/json"
  	"fmt"
  	"os"
  	"strings"
  	"text/template"
  )

  type PromptTask struct {
  	ID    int64
  	Title string
  	Body  string
  }

  type PromptSubtask struct {
  	ID        int64
  	Title     string
  	State     string
  	IsBlocked bool
  	DependsOn []int64
  }

  type PromptData struct {
  	Task      PromptTask
  	Cwd       string
  	Input     string
  	Feedback  string
  	Iteration int64
  	Outcomes  []string
  	Subtasks  []PromptSubtask
  }

  // Mirrors Tend.Template.Parity.Data.sets/0.
  var datasets = map[string]PromptData{
  	"zero": {},
  	"sample": {
  		Task:      PromptTask{ID: 1, Title: "sample task", Body: "sample body"},
  		Cwd:       "/tmp/sample",
  		Input:     "sample input",
  		Feedback:  "sample feedback",
  		Iteration: 1,
  		Outcomes:  []string{"done"},
  		Subtasks: []PromptSubtask{
  			{ID: 2, Title: "sample sub-task", State: "todo"},
  			{ID: 3, Title: "sample blocked sub-task", State: "todo", IsBlocked: true, DependsOn: []int64{2}},
  		},
  	},
  	"full": {
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
  	},
  	"one":   {Outcomes: []string{"approve"}},
  	"three": {Outcomes: []string{"a", "b", "c"}},
  }

  type caseIn struct {
  	Name     string `json:"name"`
  	Template string `json:"template"`
  	Data     string `json:"data"`
  }

  type caseOut struct {
  	Name     string `json:"name"`
  	Template string `json:"template"`
  	Data     string `json:"data"`
  	OK       bool   `json:"ok"`
  	Output   string `json:"output,omitempty"`
  	Error    string `json:"error,omitempty"`
  }

  // The body of internal/workflow/prompt.go's RenderPrompt, without the
  // ErrInvalidPrompt wrapping the caller adds.
  func render(promptMD string, data PromptData) (string, error) {
  	tmpl, err := template.New("prompt").Option("missingkey=error").Parse(promptMD)
  	if err != nil {
  		return "", err
  	}
  	var sb strings.Builder
  	if err := tmpl.Execute(&sb, data); err != nil {
  		return "", err
  	}
  	return sb.String(), nil
  }

  func main() {
  	if len(os.Args) != 2 {
  		fmt.Fprintln(os.Stderr, "usage: parity CASES.jsonl")
  		os.Exit(2)
  	}
  	file, err := os.Open(os.Args[1])
  	if err != nil {
  		fmt.Fprintln(os.Stderr, err)
  		os.Exit(1)
  	}
  	defer file.Close()

  	lines := bufio.NewScanner(file)
  	lines.Buffer(make([]byte, 1024*1024), 64*1024*1024)
  	out := bufio.NewWriter(os.Stdout)
  	defer out.Flush()

  	for lines.Scan() {
  		line := strings.TrimSpace(lines.Text())
  		if line == "" {
  			continue
  		}
  		var in caseIn
  		if err := json.Unmarshal([]byte(line), &in); err != nil {
  			fmt.Fprintln(os.Stderr, "bad case:", err)
  			os.Exit(1)
  		}
  		data, ok := datasets[in.Data]
  		if !ok {
  			fmt.Fprintln(os.Stderr, "unknown data set:", in.Data)
  			os.Exit(1)
  		}
  		result := caseOut{Name: in.Name, Template: in.Template, Data: in.Data}
  		if got, err := render(in.Template, data); err != nil {
  			result.Error = err.Error()
  		} else {
  			result.OK = true
  			result.Output = got
  		}
  		encoded, err := json.Marshal(result)
  		if err != nil {
  			fmt.Fprintln(os.Stderr, "bad result:", err)
  			os.Exit(1)
  		}
  		out.Write(encoded)
  		out.WriteString("\n")
  	}
  	if err := lines.Err(); err != nil {
  		fmt.Fprintln(os.Stderr, err)
  		os.Exit(1)
  	}
  }
  """

  @doc """
  Whether a Go toolchain is on the `PATH` to render against.

  Setting `TEND_PARITY_NO_GO=1` makes this answer `false` on a machine that
  does have Go. That is not a convenience: the Go-less path is the one where
  the suite still goes green, so it is the path most in need of being
  exercised, and without an override it can only ever be tested on a runner
  that cannot run the rest of the harness either.
  """
  @spec available?() :: boolean()
  def available? do
    System.get_env("TEND_PARITY_NO_GO") in [nil, ""] and System.find_executable("go") != nil
  end

  @doc """
  Renders `cases` through Go and returns one result map per case, in order.

  A case is `%{name: .., template: .., data: ..}`; a result is
  `%{"name" => .., "template" => .., "data" => .., "ok" => .., "output" => ..}`
  or the same with `"error"`. Raises when the Go toolchain is missing or the
  driver fails to build.
  """
  @spec render([map()]) :: [map()]
  def render([]), do: []

  def render(cases) do
    unless available?() do
      raise "the Go parity driver needs a Go toolchain on the PATH"
    end

    dir = Path.join(System.tmp_dir!(), "tend-parity-#{System.unique_integer([:positive])}")

    try do
      File.mkdir_p!(dir)
      File.write!(Path.join(dir, "go.mod"), @go_mod)
      File.write!(Path.join(dir, "main.go"), @program)
      input = Path.join(dir, "cases.jsonl")
      File.write!(input, Enum.map(cases, &(JSON.encode!(&1) <> "\n")))

      # `stderr_to_stdout: false` on the happy path on purpose: stdout is the
      # JSONL this decodes, and anything Go says on stderr would corrupt it.
      case run_driver(dir, input, false) do
        {output, 0} ->
          output |> String.split("\n", trim: true) |> Enum.map(&JSON.decode!/1)

        {output, status} ->
          raise "the Go parity driver exited #{status}:\n" <> detail(dir, input, output)
      end
    after
      File.rm_rf(dir)
    end
  end

  # A driver that fails to compile says so on stderr and nowhere else, so the
  # message from the run above is empty exactly when it matters most. The
  # failure is deterministic and the driver is a stdlib-only single file, so
  # re-run it merged purely to build the message -- and fall back to the
  # original output in the one case where the re-run does not fail the same
  # way, rather than raising with a list of JSON results.
  defp detail(dir, input, fallback) do
    case run_driver(dir, input, true) do
      {merged, status} when status != 0 -> String.trim(merged)
      _recovered -> String.trim(fallback)
    end
  end

  defp run_driver(dir, input, stderr_to_stdout?) do
    System.cmd("go", ["run", ".", input],
      cd: dir,
      stderr_to_stdout: stderr_to_stdout?,
      env: [{"GOWORK", "off"}]
    )
  end
end
