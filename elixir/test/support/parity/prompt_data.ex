defmodule Tend.Template.Parity.PromptTask do
  @moduledoc """
  `internal/workflow`'s `PromptTask`, field for field and in declaration
  order, so `{{.Task}}` prints what Go prints.
  """

  @type t :: %__MODULE__{id: integer(), title: binary(), body: binary()}

  defstruct id: 0, title: "", body: ""
end

defmodule Tend.Template.Parity.PromptSubtask do
  @moduledoc """
  `internal/workflow`'s `PromptSubtask`. Declaration order matters here too:
  `{{.Subtasks}}` prints each struct's fields in it.
  """

  @type t :: %__MODULE__{
          id: integer(),
          title: binary(),
          state: binary(),
          is_blocked: boolean(),
          depends_on: [integer()]
        }

  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end

defmodule Tend.Template.Parity.PromptData do
  @moduledoc """
  `internal/workflow`'s `PromptData` -- the variable set every `prompt_md` is
  rendered against.

  An empty string field is `""` and an empty list field is `[]`, never `nil`,
  because that is what a zero-valued Go struct holds and
  `Tend.Template.Renderer` renders the two differently.
  """

  alias Tend.Template.Parity.PromptSubtask
  alias Tend.Template.Parity.PromptTask

  @type t :: %__MODULE__{
          task: PromptTask.t(),
          cwd: binary(),
          input: binary(),
          feedback: binary(),
          iteration: integer(),
          outcomes: [binary()],
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
