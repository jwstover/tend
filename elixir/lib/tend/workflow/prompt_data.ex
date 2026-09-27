defmodule Tend.Workflow.PromptData do
  @moduledoc """
  The variable set a step prompt is rendered against -- a port of `PromptData`
  in `internal/workflow/prompt.go`.

  Every field is a template variable; anything else is a render error, because
  `Tend.Workflow.Prompt` renders with Go's `missingkey=error` semantics:

      {{.Task.Title}} {{.Task.Body}} {{.Task.ID}}
      {{.Cwd}}
      {{.Input}}      previous step's deliverable, "" for the first step
      {{.Feedback}}   deliverable of the step that routed here on a
                      reject-style outcome, "" otherwise
      {{.Iteration}}  1-based count of this step within the run
      {{.Outcomes}}   allowed outcomes for this step, so a prompt can tell
                      the agent what finish_step accepts
      {{.Subtasks}}   the task's direct sub-tasks, oldest first, each with
                      ID, Title, State, IsBlocked and DependsOn; empty when
                      the task has none, so {{range .Subtasks}} renders nothing

  This stays its own type rather than reusing `Tend.Task`, exactly as the Go
  comment on `PromptTask` explains: the template vocabulary is a contract with
  every `prompt_md` already stored in someone's `tend.db`, so it must not move
  when the task struct does.

  A template names those variables in Go's spelling -- `{{.Task.Title}}`, not
  `{{.task.title}}` -- and `Tend.Template.Renderer` underscores each name
  before looking it up, so the stored prompts keep working untouched.

  Two field shapes are contractual, not stylistic. An empty string is `""` and
  never `nil`, and an empty list is `[]` and never `nil`: a zero-valued Go
  struct holds the first of each pair, and `Tend.Template.Value` prints `nil`
  as `<no value>`. The defaults below are the zero-valued struct, so a
  `%Tend.Workflow.PromptData{}` renders what Go's `PromptData{}` renders.
  """

  alias Tend.Workflow.PromptSubtask
  alias Tend.Workflow.PromptTask

  @typedoc "The prompt variables, field for field the Go struct."
  @type t :: %__MODULE__{
          task: PromptTask.t(),
          cwd: String.t(),
          input: String.t(),
          feedback: String.t(),
          iteration: integer(),
          outcomes: [String.t()],
          subtasks: [PromptSubtask.t()]
        }

  defstruct task: %PromptTask{},
            cwd: "",
            input: "",
            feedback: "",
            iteration: 0,
            outcomes: [],
            subtasks: []
end
