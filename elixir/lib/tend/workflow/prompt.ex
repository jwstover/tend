defmodule Tend.Workflow.Prompt do
  @moduledoc """
  Rendering and validating a step's `prompt_md` -- a port of
  `internal/workflow/prompt.go`.

  A step's prompt is a Go `text/template` rendered per run against a
  `Tend.Workflow.PromptData`. `render/2` is Go's `RenderPrompt` and
  `validate/1` its `ValidatePrompt`; the language itself is `Tend.Template`,
  whose parity with Go's engine is settled by the corpus in
  `Tend.Template.Parity` rather than asserted here.

  ## Unknown variables are an error, never silently empty

  Go parses with `Option("missingkey=error")`, so a typo in a prompt surfaces
  the first time it is rendered rather than as a confused agent.
  `Tend.Template` refuses a name the data has not got whatever that option
  would have said, which is the same behaviour over the one data shape this
  module renders against.

  ## The error is one atom carrying one message

  Both failures -- a template that does not parse and one that does not fit its
  data -- come back as

      {:error, {:invalid_prompt, cause}}

  where `:invalid_prompt` is `ErrInvalidPrompt`'s atom and `cause` is the
  template engine's own exception (`Tend.Template.ParseError` or
  `Tend.Template.RenderError`). `Tend.Error.message/1` renders the pair the way
  Go's `fmt.Errorf("%w: %w", ErrInvalidPrompt, err)` does:

      invalid prompt template: template: prompt:1:9: executing "prompt" at <.Nope>: ...

  The cause is carried rather than flattened to a string because the TUI's
  validate action and the launch path show that message as-is, and because a
  caller that wants to tell a parse failure from a render failure can match the
  struct instead of the text. It is one byte longer than Go's in one place:
  `Tend.Template.ParseError` reports a column and a byte offset where Go
  reports a line, on purpose, and that module says why.

  ## No caller yet

  The runner that renders a step's prompt is Phase 3, and
  `Tend.Workflow.Graph.validate/3` takes its prompt check as a parameter -- so
  `validate_message/1` below is the adapter that module's doc anticipates, and
  nothing in the tree passes it yet. Pure functions, no I/O, fully tested.
  """

  alias Tend.Error
  alias Tend.Template
  alias Tend.Template.ParseError
  alias Tend.Template.RenderError
  alias Tend.Workflow
  alias Tend.Workflow.PromptData
  alias Tend.Workflow.PromptSubtask
  alias Tend.Workflow.PromptTask

  @typedoc """
  What a prompt that will not render comes back as: `ErrInvalidPrompt`'s atom
  and the template engine's own exception.
  """
  @type invalid :: {:invalid_prompt, ParseError.t() | RenderError.t()}

  @doc """
  Renders `prompt_md` against `data`.

  Go's `RenderPrompt`. A template that parses and fits its data renders to Go's
  bytes; anything else is `{:error, {:invalid_prompt, cause}}`.

      iex> task = %Tend.Workflow.PromptTask{id: 42, title: "Fix the flaky test"}
      iex> data = %Tend.Workflow.PromptData{task: task}
      iex> Tend.Workflow.Prompt.render("Task {{.Task.ID}}: {{.Task.Title}}", data)
      {:ok, "Task 42: Fix the flaky test"}

      iex> {:error, {:invalid_prompt, cause}} =
      ...>   Tend.Workflow.Prompt.render("Hello {{.Nope}}", %Tend.Workflow.PromptData{})
      iex> Exception.message(cause) =~ "Nope"
      true
  """
  @spec render(String.t(), PromptData.t()) :: {:ok, String.t()} | {:error, invalid()}
  def render(prompt_md, %PromptData{} = data) when is_binary(prompt_md) do
    wrap(Template.render(prompt_md, data))
  end

  @doc """
  Checks a prompt at authoring time, before any run exists to render it
  against.

  Go's `ValidatePrompt`, and the same two executions: the template is parsed
  once and rendered against a zero `Tend.Workflow.PromptData` and against
  `sample_data/0`, so both arms of the usual
  `{{if .Feedback}}...{{else}}...{{end}}` are walked and an unknown variable
  inside either is caught.

      iex> Tend.Workflow.Prompt.validate("Do it.{{if .Feedback}} Fix: {{.Feedback}}{{end}}")
      :ok

      iex> match?({:error, {:invalid_prompt, _cause}}, Tend.Workflow.Prompt.validate("{{.Cwdd}}"))
      true
  """
  @spec validate(String.t()) :: :ok | {:error, invalid()}
  def validate(prompt_md) when is_binary(prompt_md) do
    with {:ok, nodes} <- wrap(Template.parse(prompt_md)) do
      Enum.reduce_while([%PromptData{}, sample_data()], :ok, fn data, :ok ->
        case wrap(Template.render(prompt_md, nodes, data)) do
          {:ok, _output} -> {:cont, :ok}
          {:error, reason} -> {:halt, {:error, reason}}
        end
      end)
    end
  end

  @doc """
  `validate/1` with its reason rendered, for `Tend.Workflow.Graph.validate/3`.

  The graph's `:prompt_validator` seam wants `(prompt_md -> :ok | {:error,
  message})`, where the message becomes the problem's text -- a problem is
  text, where a `Tend.Error` reason is never a string. This is the one-line
  adapter between the two conventions that `Tend.Workflow.Graph`'s module doc
  describes, and the message is the one Go's `add(st, "%v", err)` writes.

      iex> Tend.Workflow.Prompt.validate_message("Do the thing.")
      :ok

      iex> {:error, message} = Tend.Workflow.Prompt.validate_message("{{.Cwdd}}")
      iex> message =~ "invalid prompt template: "
      true
  """
  @spec validate_message(String.t()) :: :ok | {:error, String.t()}
  def validate_message(prompt_md) when is_binary(prompt_md) do
    case validate(prompt_md) do
      :ok -> :ok
      {:error, reason} -> {:error, Error.message(reason)}
    end
  end

  @doc """
  The fully populated data `validate/1` renders against, Go's
  `samplePromptData`.

  Every field is non-zero so validation walks the truthy branch of any
  conditional, and the sub-tasks mix a ready one with a blocked one so a
  `{{range .Subtasks}}` body that reads `is_blocked` or `depends_on` is
  exercised both ways. The zero-valued `Tend.Workflow.PromptData` that
  `validate/1` also renders against covers the empty list.

  Public because it is data, and because the parity corpus renders the repo's
  every template against it -- `Tend.Template.Parity.Data`'s `"sample"` set is
  this value and the Go driver's is `samplePromptData`, so the corpus compares
  the two byte for byte.
  """
  @spec sample_data() :: PromptData.t()
  def sample_data do
    %PromptData{
      task: %PromptTask{id: 1, title: "sample task", body: "sample body"},
      cwd: "/tmp/sample",
      input: "sample input",
      feedback: "sample feedback",
      iteration: 1,
      outcomes: [Workflow.outcome_done()],
      subtasks: [
        %PromptSubtask{id: 2, title: "sample sub-task", state: "todo"},
        %PromptSubtask{
          id: 3,
          title: "sample blocked sub-task",
          state: "todo",
          is_blocked: true,
          depends_on: [2]
        }
      ]
    }
  end

  # Go wraps whatever the template engine returns in ErrInvalidPrompt, parse
  # failures and render failures alike, and so does this.
  defp wrap({:ok, value}), do: {:ok, value}
  defp wrap({:error, cause}), do: {:error, {:invalid_prompt, cause}}
end
