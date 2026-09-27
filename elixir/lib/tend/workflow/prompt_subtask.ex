defmodule Tend.Workflow.PromptSubtask do
  @moduledoc """
  One direct sub-task of the run's task as a prompt template sees it -- a port
  of `PromptSubtask` in `internal/workflow/prompt.go`.

  `is_blocked` says whether any task it waits on is still open; `depends_on`
  lists every task it waits on, open or done, so a prompt can spell out the
  ready set ("sub-tasks not done and not blocked") rather than the agent having
  to call `list_subtasks` to work it out.

  Field order is Go's, for the reason `Tend.Workflow.PromptTask` gives: a bare
  `{{.Subtasks}}` prints each struct's fields in declaration order.

  `depends_on` is `[]` and never `nil`, the way a zero-valued Go struct's nil
  slice prints -- `Tend.Template.Value` tells the two apart.
  """

  @typedoc "A sub-task as a prompt sees it, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          title: String.t(),
          state: String.t(),
          is_blocked: boolean(),
          depends_on: [integer()]
        }

  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end
