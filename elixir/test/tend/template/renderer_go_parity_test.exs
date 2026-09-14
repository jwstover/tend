defmodule Tend.Template.GoParityTest.PromptTask do
  @moduledoc false
  defstruct id: 0, title: "", body: ""
end

defmodule Tend.Template.GoParityTest.PromptSubtask do
  @moduledoc false
  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end

defmodule Tend.Template.GoParityTest.PromptData do
  @moduledoc false
  defstruct task: %Tend.Template.GoParityTest.PromptTask{},
            cwd: "",
            input: "",
            feedback: "",
            iteration: 0,
            outcomes: [],
            subtasks: []
end

defmodule Tend.Template.GoParityTest do
  # Written by the review of sub-task #301 and kept as a regression test: the
  # four cases below are the ones where the port's claims about Go were wrong
  # rather than its code. Every `want` was captured from Go 1.26.4's
  # text/template with Option("missingkey=error"), against the same data as
  # internal/workflow/prompt_test.go's fullData.
  use ExUnit.Case, async: true

  alias Tend.Template
  alias Tend.Template.GoParityTest.PromptData
  alias Tend.Template.GoParityTest.PromptSubtask
  alias Tend.Template.GoParityTest.PromptTask

  @full %PromptData{
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

  describe "struct declaration order is recoverable, so the sort is not forced" do
    test "defstruct order survives to runtime via __info__(:struct)" do
      # This is why Tend.Template.Renderer can print a struct the way Go's %v
      # does: defstruct records declaration order and __info__(:struct) hands
      # it back, in order, for any struct. Sorting the fields by name instead
      # would put :state before :title.
      assert Enum.map(PromptSubtask.__info__(:struct), & &1.field) ==
               [:id, :title, :state, :is_blocked, :depends_on]

      assert Enum.map(PromptData.__info__(:struct), & &1.field) ==
               [:task, :cwd, :input, :feedback, :iteration, :outcomes, :subtasks]
    end

    test "so a bare {{.Subtasks}} is byte-identical to Go" do
      # Go 1.26.4, template "{{.Subtasks}}" against fullData:
      #
      #   go run . => "[{43 write the migration done false []} ..."
      #
      # This is a prompt_md literal that appears in prompt_test.go (twice:
      # TestRenderPrompt and TestValidatePrompt, the latter rendering it
      # against populated sample data), and it uses no builtin.
      assert Template.render("{{.Subtasks}}", @full) ==
               {:ok,
                "[{43 write the migration done false []} " <>
                  "{44 wire the store todo false [43]} " <>
                  "{45 expose over MCP todo true [44]}]"}
    end
  end

  describe "{{range}} over an integer" do
    test "Go 1.22+ ranges over an int, and PromptData.Iteration is one" do
      # `{{range .Iteration}}` is a perfectly ordinary stored prompt, since
      # PromptData.Iteration is an int64, so refusing it was a divergence a
      # user could reach.
      #
      # Go 1.26.4, "{{range .Iteration}}x{{end}}" against fullData => "xxx".
      assert Template.render("{{range .Iteration}}x{{end}}", @full) == {:ok, "xxx"}
    end
  end

  describe "{{range}} over a string" do
    test "Go refuses it, exactly as this does" do
      # Go's text/template does not range over a string; it errors, which is
      # what this does too. Only the map and integer cases were ever real
      # divergences, and only the map one is left.
      #
      # Go 1.26.4: range can't iterate over /home/me/proj
      assert {:error, error} = Template.render("{{range .Cwd}}x{{end}}", @full)
      assert error.detail == "range can't iterate over /home/me/proj"
    end
  end
end
