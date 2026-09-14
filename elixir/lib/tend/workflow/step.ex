defmodule Tend.Workflow.Step do
  @moduledoc """
  One node of a workflow -- a port of `workflow.Step` in
  `internal/workflow/workflow.go`.

  `prompt_md` is a Go `text/template` rendered per run by the prompt port;
  `model` and `permission_mode` are forwarded to `claude` as-is, `""` meaning
  "inherit the default". `sort_order` is authoring order only: execution order
  is defined by the edges leaving the step.

  `kind` defaults to `nil` rather than `""`: Go's zero is `StepKind("")`, which
  is not a valid kind and so has no atom here. See `Tend.Workflow.StepKind`.
  """

  alias Tend.Workflow.StepKind

  @typedoc "A step, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          workflow_id: integer(),
          name: String.t(),
          kind: StepKind.t() | nil,
          prompt_md: String.t(),
          model: String.t(),
          permission_mode: String.t(),
          sort_order: integer(),
          created_at: DateTime.t() | nil,
          updated_at: DateTime.t() | nil
        }

  defstruct id: 0,
            workflow_id: 0,
            name: "",
            kind: nil,
            prompt_md: "",
            model: "",
            permission_mode: "",
            sort_order: 0,
            created_at: nil,
            updated_at: nil
end
