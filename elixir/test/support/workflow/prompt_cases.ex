defmodule Tend.Workflow.PromptCases do
  @moduledoc """
  `internal/workflow/prompt_test.go`'s tables, in one place.

  Two test modules need them and neither may reach into the other: the ported
  unit test `Tend.Workflow.PromptTest`, which asserts each case's `want`
  exactly as the Go table writes it, and `Tend.Workflow.GoDriverTest`, which
  renders every template here through Go's own `RenderPrompt` and compares the
  bytes. A single copy is what makes the second a check on the first rather
  than on a second transcription of the Go source.

  The data sets are `Tend.Workflow.PromptData` values rather than a mirror of
  one, because the point of the parity run is to settle what the *shipped*
  struct renders to.
  """

  alias Tend.Workflow.Prompt
  alias Tend.Workflow.PromptData
  alias Tend.Workflow.PromptSubtask
  alias Tend.Workflow.PromptTask

  @doc "prompt_test.go's `fullData`."
  @spec full() :: PromptData.t()
  def full do
    %PromptData{
      task: %PromptTask{id: 42, title: "Fix the flaky test", body: "It fails on CI only."},
      cwd: "/home/me/proj",
      input: "previous deliverable",
      feedback: "reviewer said no",
      iteration: 3,
      outcomes: ["approve", "reject"],
      subtasks: [
        %PromptSubtask{id: 43, title: "write the migration", state: "done"},
        %PromptSubtask{id: 44, title: "wire the store", state: "todo", depends_on: [43]},
        %PromptSubtask{
          id: 45,
          title: "expose over MCP",
          state: "todo",
          is_blocked: true,
          depends_on: [44]
        }
      ]
    }
  end

  @doc """
  Every data set a case can name, by the name the Go driver knows it by.

  `"zero"` and `"full"` are the empty and populated `PromptData` the sub-task
  asks every case to be rendered against; `"sample"` is `ValidatePrompt`'s
  other half, and `"task_only"` and `"iteration_only"` are the two partial
  values the Go render table names for a single case each.
  """
  @spec data_sets() :: %{optional(binary()) => PromptData.t()}
  def data_sets do
    %{
      "zero" => %PromptData{},
      "full" => full(),
      "sample" => Prompt.sample_data(),
      "task_only" => %PromptData{task: %PromptTask{id: 1}},
      "iteration_only" => %PromptData{iteration: 1},
      "feedback_only" => %PromptData{feedback: "make it faster"}
    }
  end

  @doc "The pair every case is additionally rendered against: empty, then populated."
  @spec both_data_sets() :: [binary()]
  def both_data_sets, do: ["zero", "full"]

  @doc """
  `readySetTpl`: the Dispatch step's job in template form -- the sub-tasks that
  are neither done nor waiting on an open one.
  """
  @spec ready_set_tpl() :: binary()
  def ready_set_tpl do
    ~S|{{range .Subtasks}}{{if and (ne .State "done") (not .IsBlocked)}}- #{{.ID}} {{.Title}}| <>
      "\n{{end}}{{end}}"
  end

  @doc """
  `TestRenderPrompt`'s table: `%{name:, template:, data:, want:}` for the cases
  Go renders, where `want` is Go's own expectation.
  """
  @spec render_cases() :: [map()]
  def render_cases do
    [
      %{
        name: "no variables (the POC case) passes through verbatim",
        template: "Fix the bug described in the task.\n\nRun the tests.",
        data: "zero",
        want: "Fix the bug described in the task.\n\nRun the tests."
      },
      %{name: "empty prompt renders empty", template: "", data: "full", want: ""},
      %{
        name: "every variable",
        template:
          ~S|Task #{{.Task.ID}}: {{.Task.Title}}| <>
            "\n{{.Task.Body}}\n" <>
            "cwd={{.Cwd}} input={{.Input}} feedback={{.Feedback}} iteration={{.Iteration}}\n" <>
            "outcomes: {{range $i, $o := .Outcomes}}{{if $i}}, {{end}}{{$o}}{{end}}",
        data: "full",
        want:
          "Task #42: Fix the flaky test\nIt fails on CI only.\n" <>
            "cwd=/home/me/proj input=previous deliverable feedback=reviewer said no iteration=3\n" <>
            "outcomes: approve, reject"
      },
      %{
        name: "first step: empty input and feedback render as nothing, not <nil>",
        template: "[{{.Input}}][{{.Feedback}}]",
        data: "iteration_only",
        want: "[][]"
      },
      %{
        name: "conditional feedback block",
        template: "Do it.{{if .Feedback}} Address this feedback: {{.Feedback}}{{end}}",
        data: "feedback_only",
        want: "Do it. Address this feedback: make it faster"
      },
      %{
        name: "range over sub-tasks with every field",
        template:
          ~S<{{range .Subtasks}}#{{.ID}} {{.Title}} [{{.State}}] > <>
            "blocked={{.IsBlocked}} deps={{.DependsOn}}\n{{end}}",
        data: "full",
        want:
          "#43 write the migration [done] blocked=false deps=[]\n" <>
            "#44 wire the store [todo] blocked=false deps=[43]\n" <>
            "#45 expose over MCP [todo] blocked=true deps=[44]\n"
      },
      %{
        name: "ready set: not done and not blocked",
        template: ready_set_tpl(),
        data: "full",
        want: "- #44 wire the store\n"
      },
      %{
        name: "no sub-tasks: range renders nothing, not an error",
        template: "sub-tasks:{{range .Subtasks}} {{.Title}}{{end}}",
        data: "task_only",
        want: "sub-tasks:"
      },
      %{
        name: "no sub-tasks: bare {{.Subtasks}} renders as an empty list",
        template: "{{.Subtasks}}",
        data: "zero",
        want: "[]"
      },
      %{
        name: "sub-task count with len",
        template: "{{len .Subtasks}} sub-tasks",
        data: "full",
        want: "3 sub-tasks"
      }
    ]
  end

  @doc """
  `TestRenderPrompt`'s refusing half: the cases Go answers with
  `ErrInvalidPrompt`, all against `fullData`.
  """
  @spec render_error_cases() :: [map()]
  def render_error_cases do
    [
      %{
        name: "unknown sub-task field is an error",
        template: "{{range .Subtasks}}{{.Body}}{{end}}",
        data: "full"
      },
      %{
        name: "unknown top-level variable is an error",
        template: "Hello {{.Nope}}",
        data: "full"
      },
      %{name: "unknown nested variable is an error", template: "{{.Task.BodyMD}}", data: "full"},
      %{name: "syntax error is an error", template: "{{.Task.Title", data: "full"},
      # TestRenderPromptErrorNamesTheVariable's template, which the Go test
      # asserts names `Tittle`.
      %{
        name: "the error names the offending variable",
        template: "{{.Task.Title}} {{.Tittle}}",
        data: "full"
      }
    ]
  end

  @doc "`TestValidatePrompt`'s table: `%{name:, template:, want: :ok | :error}`."
  @spec validate_cases() :: [map()]
  def validate_cases do
    [
      %{name: "plain text", template: "Just do the thing.", want: :ok},
      %{
        name: "every variable",
        template:
          "{{.Task.ID}}{{.Task.Title}}{{.Task.Body}}{{.Cwd}}" <>
            "{{.Input}}{{.Feedback}}{{.Iteration}}{{.Outcomes}}",
        want: :ok
      },
      %{name: "range over outcomes", template: "{{range .Outcomes}}- {{.}}\n{{end}}", want: :ok},
      %{name: "bare subtasks", template: "{{.Subtasks}}", want: :ok},
      %{
        name: "range over sub-tasks with every field",
        template:
          "{{range .Subtasks}}{{.ID}}{{.Title}}{{.State}}{{.IsBlocked}}{{.DependsOn}}{{end}}",
        want: :ok
      },
      %{name: "ready-set template", template: ready_set_tpl(), want: :ok},
      %{
        name: "range over each sub-task's dependencies",
        template: ~S|{{range .Subtasks}}{{range .DependsOn}}#{{.}} {{end}}{{end}}|,
        want: :ok
      },
      %{
        name: "unknown sub-task field",
        template: "{{range .Subtasks}}{{.Body}}{{end}}",
        want: :error
      },
      %{
        name: "unknown field inside blocked branch",
        template: "{{range .Subtasks}}{{if .IsBlocked}}{{.Blockers}}{{end}}{{end}}",
        want: :error
      },
      %{
        name: "unknown field inside unblocked branch",
        template: "{{range .Subtasks}}{{if .IsBlocked}}ok{{else}}{{.Blockers}}{{end}}{{end}}",
        want: :error
      },
      %{name: "unknown variable", template: "{{.Cwdd}}", want: :error},
      %{
        name: "unknown variable inside truthy branch",
        template: "{{if .Feedback}}{{.Bogus}}{{end}}",
        want: :error
      },
      %{
        name: "unknown variable inside falsy branch",
        template: "{{if .Feedback}}ok{{else}}{{.Bogus}}{{end}}",
        want: :error
      },
      %{name: "unclosed action", template: "{{.Cwd", want: :error}
    ]
  end

  @doc """
  Every template prompt_test.go writes, once each: the render table, its
  refusing half, and the validate table.
  """
  @spec templates() :: [binary()]
  def templates do
    (render_cases() ++ render_error_cases() ++ validate_cases())
    |> Enum.map(& &1.template)
    |> Enum.uniq()
  end
end
