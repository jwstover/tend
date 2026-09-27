defmodule Tend.Workflow.PromptTest do
  @moduledoc """
  A port of `internal/workflow/prompt_test.go`.

  The tables are in `Tend.Workflow.PromptCases`, shared with
  `Tend.Workflow.GoDriverTest`, so the byte-parity run checks *these* cases
  rather than a second transcription of the Go source. Every `want` below is
  the Go test's own expectation, character for character.

  The Go table names one data set per case; the sub-task asks for both an empty
  and a populated `Tend.Workflow.PromptData`, since that pair is what pins the
  `missingkey=error` behaviour -- a name missing from only one branch of an
  `{{if}}` is invisible to a single render. The second render has no `want`
  here, so what it produces is pinned against Go in the parity test; what this
  file settles is the verdict.
  """

  use ExUnit.Case, async: true

  alias Tend.Error
  alias Tend.Workflow.Graph
  alias Tend.Workflow.Prompt
  alias Tend.Workflow.PromptCases
  alias Tend.Workflow.PromptData
  alias Tend.Workflow.PromptSubtask
  alias Tend.Workflow.PromptTask
  alias Tend.Workflow.Step

  doctest Tend.Workflow.Prompt

  defp data(name), do: Map.fetch!(PromptCases.data_sets(), name)

  describe "render/2" do
    for %{name: name, template: template, data: set, want: want} <- PromptCases.render_cases() do
      test name do
        assert Prompt.render(unquote(template), data(unquote(set))) == {:ok, unquote(want)}
      end
    end

    for %{name: name, template: template, data: set} <- PromptCases.render_error_cases() do
      test "refuses: #{name}" do
        assert {:error, {:invalid_prompt, cause}} =
                 Prompt.render(unquote(template), data(unquote(set)))

        assert is_exception(cause)
      end
    end

    test "answers every case against an empty PromptData as well as a populated one" do
      # The bytes are Go's business (Tend.Workflow.GoDriverTest); what this
      # asserts is that neither render raises and that a template Go renders
      # against both is rendered against both here.
      for template <- PromptCases.templates(), set <- PromptCases.both_data_sets() do
        assert match?({:ok, output} when is_binary(output), Prompt.render(template, data(set))) or
                 match?({:error, {:invalid_prompt, _cause}}, Prompt.render(template, data(set)))
      end
    end

    test "the error names the offending variable" do
      # TestRenderPromptErrorNamesTheVariable.
      assert {:error, {:invalid_prompt, cause} = reason} =
               Prompt.render("{{.Task.Title}} {{.Tittle}}", data("full"))

      assert Exception.message(cause) =~ "Tittle"
      assert Error.message(reason) =~ "Tittle"
    end

    test "the error names the offending variable's position too" do
      # Go reports a line and no more; Tend.Template.ParseError and
      # Tend.Template.RenderError add the column and the byte offset, which the
      # sub-task asks for. Those two modules say why they diverge here.
      assert {:error, {:invalid_prompt, cause}} = Prompt.render("Hello {{.Nope}}", data("full"))
      assert %{line: 1, column: 9, offset: 8} = cause

      assert Exception.message(cause) ==
               ~s(template: prompt:1:9: executing "prompt" at <.Nope>: ) <>
                 "can't evaluate field Nope in type Tend.Workflow.PromptData"
    end

    test "the whole message is ErrInvalidPrompt's text, then the engine's" do
      assert {:error, reason} = Prompt.render("{{.Cwdd}}", data("full"))

      assert Error.message(reason) ==
               "invalid prompt template: template: prompt:1:3: " <>
                 ~s(executing "prompt" at <.Cwdd>: ) <>
                 "can't evaluate field Cwdd in type Tend.Workflow.PromptData"
    end

    test "a parse failure wraps the same way a render failure does" do
      assert {:error, reason} = Prompt.render("{{.Task.Title", data("full"))

      assert Error.message(reason) ==
               "invalid prompt template: template: prompt:1:14: unclosed action at byte 13"
    end
  end

  describe "validate/1" do
    for %{name: name, template: template, want: :ok} <- PromptCases.validate_cases() do
      test "accepts #{name}" do
        assert Prompt.validate(unquote(template)) == :ok
      end
    end

    for %{name: name, template: template, want: :error} <- PromptCases.validate_cases() do
      test "refuses #{name}" do
        assert {:error, {:invalid_prompt, _cause}} = Prompt.validate(unquote(template))
      end
    end

    test "renders against both the zero data and the sample, as Go's ValidatePrompt does" do
      # The two executions are the whole point of ValidatePrompt: a name
      # missing from only the truthy branch is caught by the sample, and one
      # missing from only the falsy branch by the zero value. Each of these
      # renders cleanly against one of the pair, so a validator running either
      # alone would pass one of them.
      truthy = "{{if .Feedback}}{{.Bogus}}{{end}}"
      falsy = "{{if .Feedback}}ok{{else}}{{.Bogus}}{{end}}"

      assert {:ok, ""} = Prompt.render(truthy, %PromptData{})
      assert {:error, _reason} = Prompt.render(truthy, Prompt.sample_data())
      assert {:ok, "ok"} = Prompt.render(falsy, Prompt.sample_data())
      assert {:error, _reason} = Prompt.render(falsy, %PromptData{})

      assert {:error, {:invalid_prompt, _cause}} = Prompt.validate(truthy)
      assert {:error, {:invalid_prompt, _cause}} = Prompt.validate(falsy)
    end
  end

  describe "sample_data/0" do
    test "is Go's samplePromptData, field for field" do
      assert Prompt.sample_data() == %PromptData{
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

    test "has every field non-zero, so validation walks every truthy branch" do
      for {_field, value} <- Map.from_struct(Prompt.sample_data()) do
        refute value in ["", 0, [], nil]
      end
    end

    test "mixes a ready sub-task with a blocked one" do
      # So a {{range .Subtasks}} body reading IsBlocked or DependsOn is walked
      # both ways; the zero PromptData validate/1 also renders against covers
      # the empty list.
      assert [%PromptSubtask{is_blocked: false, depends_on: []}, %PromptSubtask{is_blocked: true}] =
               Prompt.sample_data().subtasks
    end
  end

  describe "validate_message/1" do
    test "passes a good prompt through" do
      assert Prompt.validate_message("Do the thing.") == :ok
    end

    test "renders the reason for Tend.Workflow.Graph's problem text" do
      assert {:error, message} = Prompt.validate_message("{{.Cwdd}}")
      assert String.starts_with?(message, "invalid prompt template: ")
      assert message =~ "Cwdd"
    end

    test "is what Tend.Workflow.Graph's :prompt_validator seam wants" do
      # The adapter Tend.Workflow.Graph's module doc anticipates: a problem is
      # text, so the graph gets a rendered message rather than a reason. This
      # is the seam's first real user; nothing in the tree passes it yet.
      steps = [%Step{id: 1, name: "implement", kind: :agent, prompt_md: "{{.Cwdd}}"}]
      problems = Graph.validate(steps, [], prompt_validator: &Prompt.validate_message/1)

      assert Enum.any?(problems, &String.starts_with?(&1.msg, "invalid prompt template: "))
    end
  end

  describe "the value types" do
    test "a zero PromptData is Go's zero value: no nil strings, no nil lists" do
      # Contractual, not stylistic: Tend.Template.Value prints nil as
      # <no value>, where Go's zero struct prints "" and [].
      zero = %PromptData{}

      assert zero.cwd == ""
      assert zero.input == ""
      assert zero.feedback == ""
      assert zero.iteration == 0
      assert zero.outcomes == []
      assert zero.subtasks == []
      assert zero.task == %PromptTask{id: 0, title: "", body: ""}
      assert %PromptSubtask{}.depends_on == []
    end
  end
end
