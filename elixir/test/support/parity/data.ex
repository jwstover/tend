defmodule Tend.Template.Parity.Data do
  @moduledoc """
  The `PromptData` values every parity case is rendered against.

  These mirror, name for name and field for field, the `datasets` map in the
  Go driver `Tend.Template.Parity.Go` embeds. The two have to agree or the
  comparison is meaningless, which is why they sit next to each other and why
  a drift shows up as a parity failure on the very first case.

    * `zero` and `sample` are the pair `ValidatePrompt` runs in
      `internal/workflow/prompt.go`, so a prompt that validates in the Go tree
      is exercised here against exactly the same two values
    * `full` is `prompt_test.go`'s `fullData`, the one the Go render tests
      assert against
    * `one` and `three` differ from `full` only in `Outcomes`, so a
      `{{range $i, $o := .Outcomes}}` can be checked at zero, one and several
      elements

  ## What this corpus cannot reach

  The Go side of the comparison is a *typed* struct, and that is not a detail
  of the harness -- it is what `internal/workflow`'s `PromptData` is. So every
  field here has a type on both sides, and a whole class of divergence is out
  of the corpus's reach by construction:

    * **an Elixir `nil` field.** Go's zero value for `Outcomes []string` is a
      nil slice, which prints `[]` and ranges as empty. An Elixir `nil` models
      Go's nil *interface*, which prints `<no value>`. The two are different
      Go values, so no data set can be nil on the Elixir side and nil on the
      Go side at once: these sets use `[]` and `""`, the zero-valued struct's
      spelling, and `Tend.Template.Renderer`'s `nil` paths -- the `{{range}}`
      that sub-task #321 fixed among them -- are never entered from here.
      Reverting that fix leaves this corpus green; it is
      `test/tend/template/renderer_go_parity_test.exs` that catches it.
    * **a field of a type `PromptData` has not got**, such as a map (`range`
      over a map sorts its keys) or a pointer that can be nil.
    * **anything that is not a `prompt_md`.** No `{{template}}`, `{{block}}`
      or `{{define}}`, because `RenderPrompt` parses one unnamed template and
      a stored prompt has nowhere to define another.

  Those live in `test/tend/template/renderer_go_parity_test.exs` and its
  neighbours, where a `want` captured from Go can be asserted against a plain
  map with whatever shape the case needs. What this corpus settles is the
  other question, and the one no hand-written test can: that every prompt a
  user actually has renders to the bytes Go gives it.
  """

  alias Tend.Template.Parity.PromptData
  alias Tend.Template.Parity.PromptSubtask
  alias Tend.Template.Parity.PromptTask

  @doc "Every data set, by the name a parity case refers to it by."
  @spec sets() :: %{optional(binary()) => PromptData.t()}
  def sets do
    %{
      "zero" => %PromptData{},
      "sample" => sample(),
      "full" => full(),
      "one" => %PromptData{outcomes: ["approve"]},
      "three" => %PromptData{outcomes: ["a", "b", "c"]}
    }
  end

  @doc "One data set by name. Raises when the name is not one of `sets/0`'s."
  @spec fetch!(binary()) :: PromptData.t()
  def fetch!(name), do: Map.fetch!(sets(), name)

  @doc "The names, in the order a report should list them."
  @spec names() :: [binary()]
  def names, do: ["zero", "sample", "full", "one", "three"]

  # internal/workflow/prompt.go's samplePromptData.
  defp sample do
    %PromptData{
      task: %PromptTask{id: 1, title: "sample task", body: "sample body"},
      cwd: "/tmp/sample",
      input: "sample input",
      feedback: "sample feedback",
      iteration: 1,
      outcomes: ["done"],
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

  # internal/workflow/prompt_test.go's fullData.
  defp full do
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
end
