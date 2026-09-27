defmodule Tend.Workflow.PromptTask do
  @moduledoc """
  The slice of a task a prompt template can see -- a port of `PromptTask` in
  `internal/workflow/prompt.go`.

  Its own type rather than `Tend.Task`, exactly as Go's is its own type rather
  than `task.Task`: the template vocabulary (`{{.Task.Body}}`) stays stable
  even if the task struct grows or renames fields, and the workflow tree keeps
  depending on nothing.

  Field order is Go's declaration order, and that is load-bearing rather than
  tidy: `Tend.Template.Value` prints a struct's fields in declaration order to
  match Go's `%v`, so a bare `{{.Task}}` in a stored prompt renders Go's bytes
  only while the two agree.
  """

  @typedoc "A task as a prompt sees it, field for field the Go struct."
  @type t :: %__MODULE__{id: integer(), title: String.t(), body: String.t()}

  defstruct id: 0, title: "", body: ""
end
