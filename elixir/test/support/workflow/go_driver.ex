defmodule Tend.Workflow.GoDriver do
  @moduledoc """
  Drives `internal/workflow`'s own `RenderPrompt`, `ValidatePrompt`,
  `StepSystemPrompt`, `NudgePrompt`, `RetryPrompt` and `FallbackAllowed`, so
  the port can be compared to them byte for byte.

  ## Why this one imports the Go package rather than copying it

  `Tend.Template.Parity.Go` embeds a *copy* of `PromptData` and of
  `RenderPrompt`'s body, because what it is settling is a question about Go's
  `text/template`, and a self-contained driver can be run from anywhere. This
  one is settling a question about `internal/workflow` itself -- does
  `Tend.Workflow.Handoff` say what `handoff.go` says, to the byte -- so a copy
  of the answer would be no evidence at all. It imports the real package.

  That constrains where the driver can live: Go allows an `internal/` import
  only from a package whose *import path* sits under the parent of `internal`,
  so the driver is written into a directory inside this repository rather than
  into `System.tmp_dir!/0`. The name starts with a dot, which is how Go and
  `go build ./...` are told to ignore a directory, and it is removed again in
  an `after`. Nothing is written anywhere else and nothing in the Go tree is
  touched.

  Cases go in as JSON lines and come back as JSON lines, which keeps the byte
  comparison honest: no shell quoting anywhere near a template or a prompt.

  The one thing JSON cannot carry is a byte that is not valid UTF-8: Go's
  `encoding/json` replaces it with U+FFFD. Every case here is a prompt or a
  step name a person typed, so this has never fired; the fix, if it ever does,
  is to base64 the `out` field.
  """

  @program ~S"""
  // Runs internal/workflow's prompt and hand-off functions over a JSONL case
  // file, for comparison against the Elixir port. Written out and run by
  // Tend.Workflow.GoDriver; never checked in, never part of `go build ./...`.
  package main

  import (
  	"bufio"
  	"encoding/json"
  	"fmt"
  	"os"
  	"strings"

  	"github.com/jwstover/tend/internal/workflow"
  )

  // Mirrors Tend.Workflow.PromptCases.data_sets/0, minus "sample": that one is
  // internal/workflow's unexported samplePromptData, which only ValidatePrompt
  // can reach, and the validate cases are what pin it.
  var datasets = map[string]workflow.PromptData{
  	"zero": {},
  	"full": {
  		Task:      workflow.PromptTask{ID: 42, Title: "Fix the flaky test", Body: "It fails on CI only."},
  		Cwd:       "/home/me/proj",
  		Input:     "previous deliverable",
  		Feedback:  "reviewer said no",
  		Iteration: 3,
  		Outcomes:  []string{"approve", "reject"},
  		Subtasks: []workflow.PromptSubtask{
  			{ID: 43, Title: "write the migration", State: "done"},
  			{ID: 44, Title: "wire the store", State: "todo", DependsOn: []int64{43}},
  			{ID: 45, Title: "expose over MCP", State: "todo", IsBlocked: true, DependsOn: []int64{44}},
  		},
  	},
  	"task_only":      {Task: workflow.PromptTask{ID: 1}},
  	"iteration_only": {Iteration: 1},
  	"feedback_only":  {Feedback: "make it faster"},
  }

  type request struct {
  	Kind      string   `json:"kind"`
  	Template  string   `json:"template"`
  	Data      string   `json:"data"`
  	Workflow  string   `json:"workflow"`
  	Step      string   `json:"step"`
  	Reason    string   `json:"reason"`
  	Iteration int64    `json:"iteration"`
  	Outcomes  []string `json:"outcomes"`
  }

  type response struct {
  	OK    bool   `json:"ok"`
  	Out   string `json:"out"`
  	Err   string `json:"err"`
  	Bool  bool   `json:"bool"`
  }

  func answer(in request) (response, error) {
  	switch in.Kind {
  	case "render":
  		data, ok := datasets[in.Data]
  		if !ok {
  			return response{}, fmt.Errorf("unknown data set %q", in.Data)
  		}
  		out, err := workflow.RenderPrompt(in.Template, data)
  		if err != nil {
  			return response{Err: err.Error()}, nil
  		}
  		return response{OK: true, Out: out}, nil
  	case "validate":
  		if err := workflow.ValidatePrompt(in.Template); err != nil {
  			return response{Err: err.Error()}, nil
  		}
  		return response{OK: true}, nil
  	case "system_prompt":
  		return response{OK: true, Out: workflow.StepSystemPrompt(workflow.HandoffContext{
  			Workflow:  in.Workflow,
  			Step:      in.Step,
  			Iteration: in.Iteration,
  			Outcomes:  in.Outcomes,
  		})}, nil
  	case "nudge":
  		return response{OK: true, Out: workflow.NudgePrompt(in.Step, in.Outcomes)}, nil
  	case "retry":
  		return response{OK: true, Out: workflow.RetryPrompt(in.Step, in.Reason, in.Outcomes)}, nil
  	case "fallback":
  		return response{OK: true, Bool: workflow.FallbackAllowed(in.Outcomes)}, nil
  	}
  	return response{}, fmt.Errorf("unknown case kind %q", in.Kind)
  }

  func main() {
  	if len(os.Args) != 2 {
  		fmt.Fprintln(os.Stderr, "usage: driver CASES.jsonl")
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
  		var in request
  		if err := json.Unmarshal([]byte(line), &in); err != nil {
  			fmt.Fprintln(os.Stderr, "bad case:", err)
  			os.Exit(1)
  		}
  		result, err := answer(in)
  		if err != nil {
  			fmt.Fprintln(os.Stderr, err)
  			os.Exit(1)
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
  Whether a Go toolchain is on the `PATH` to compare against.

  Callers use it in a module body to skip rather than fail, the way
  `Tend.Test.Go.available?/0`'s callers do.
  """
  @spec available?() :: boolean()
  def available?, do: System.find_executable("go") != nil

  @doc """
  Runs `cases` through the Go functions and returns one result map per case, in
  order.

  A case is a map with a `:kind` and that kind's arguments; a result is
  `%{"ok" => .., "out" => .., "err" => .., "bool" => ..}`. Raises when the Go
  toolchain is missing or the driver will not build.
  """
  @spec run([map()]) :: [map()]
  def run([]), do: []

  def run(cases) do
    unless available?() do
      raise "the Go workflow driver needs a Go toolchain on the PATH"
    end

    root = repo_root()
    package = ".tend-workflow-driver-#{System.unique_integer([:positive])}"
    dir = Path.join(root, package)
    input = Path.join(System.tmp_dir!(), package <> ".jsonl")

    try do
      File.mkdir_p!(dir)
      File.write!(Path.join(dir, "main.go"), @program)
      File.write!(input, Enum.map(cases, &(JSON.encode!(&1) <> "\n")))

      # stderr stays separate on the happy path: stdout is the JSONL this
      # decodes, and anything Go says on stderr would corrupt it.
      case go_run(root, package, input, false) do
        {output, 0} ->
          output |> String.split("\n", trim: true) |> Enum.map(&JSON.decode!/1)

        {output, status} ->
          raise "the Go workflow driver exited #{status}:\n" <>
                  detail(root, package, input, output)
      end
    after
      File.rm_rf(dir)
      File.rm(input)
    end
  end

  # A driver that will not compile says so on stderr and nowhere else, so the
  # message from the run above is empty exactly when it matters most. The
  # failure is deterministic, so re-run it merged purely to build the message.
  defp detail(root, package, input, fallback) do
    case go_run(root, package, input, true) do
      {merged, status} when status != 0 -> String.trim(merged)
      _recovered -> String.trim(fallback)
    end
  end

  defp go_run(root, package, input, stderr_to_stdout?) do
    System.cmd("go", ["run", "./" <> package, input],
      cd: root,
      stderr_to_stdout: stderr_to_stdout?,
      env: [{"GOWORK", "off"}]
    )
  end

  @doc "The repository root, the directory holding `go.mod`."
  @spec repo_root() :: binary()
  def repo_root do
    from_cwd = Path.expand("..", File.cwd!())

    if File.exists?(Path.join(from_cwd, "go.mod")) do
      from_cwd
    else
      Path.expand("../../../..", __DIR__)
    end
  end
end
