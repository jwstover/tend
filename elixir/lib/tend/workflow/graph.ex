defmodule Tend.Workflow.Graph do
  @moduledoc """
  The graph a workflow's edges imply, read two ways -- a port of
  `internal/workflow/graph.go`.

  As a text preview for the authoring view (`preview/2`, `preview_text/2`), and
  as a list of the things wrong with it (`validate/3`). Both are pure functions
  of the steps in authoring order and the edges between them, so the TUI
  computes them while handling a key press and a test can pin their output
  without a store.

  ## Messages are the contract

  `validate/3`'s problems are rendered verbatim by the TUI's validate action,
  so their text is part of the port, not a detail: `Tend.Workflow.GraphParity`
  cases in `test/tend/workflow/go_parity_test.exs` pin every message format
  against the Go source and fail when the two drift. Go's `Problem` carries no
  severity -- every problem is a problem -- so neither does
  `Tend.Workflow.Graph.Problem`.

  Problem order is Go's: authoring order over the steps, and within a step
  unreachability, then the missing terminal, then each leaving edge in the
  order given, then the prompt problems with the mentioned outcomes in sorted
  order. Nothing here depends on map iteration order.

  ## The prompt check is injected, and off by default

  Go's `Validate` also calls `ValidatePrompt`, which parses and executes the
  step's `prompt_md` as a `text/template` and reports
  `"invalid prompt template: ..."` when it does not render. That lives in
  `internal/workflow/prompt.go`, which this part of the port does not cover:
  the template engine and `Tend.Workflow.Prompt` land later.

  So the check is a parameter. `validate/3` takes a `:prompt_validator`
  function; the default checks nothing, which means an unrenderable prompt is
  simply not reported yet. When the prompt port lands it passes its own
  validator -- `(prompt_md -> :ok | {:error, message})`, where `message` is the
  text Go's `%v` on the wrapped `ErrInvalidPrompt` would produce -- and the
  problem appears with no change here.

  ## Divergences from Go

    * Go's `Validate(steps, edges)` is `validate/3` here, the third argument
      being the options above. There is no one-argument form: a graph is its
      steps and its edges, exactly as in Go.
    * The `(?i)` in the patterns below folds case the way PCRE does without
      `UCP`, i.e. ASCII only, where Go's RE2 folds Unicode. An outcome whose
      letters are outside ASCII is matched case-sensitively here. Same family
      of divergence as `Tend.Workflow.normalize_outcome/1`'s, and the same
      reason it is left alone: closing it would mean reimplementing a table.

  Nothing calls this module yet, exactly as nothing calls the rest of
  `Tend.Workflow`. The store and TUI ports are what will.
  """

  alias Tend.Workflow
  alias Tend.Workflow.Edge
  alias Tend.Workflow.Graph.PreviewEdge
  alias Tend.Workflow.Graph.PreviewRow
  alias Tend.Workflow.Graph.Problem
  alias Tend.Workflow.Step

  @typedoc """
  The authoring-time prompt check `validate/3` delegates to.

  `{:error, message}`'s message becomes the problem's text as-is, which is what
  Go's `add(st, "%v", err)` does with the error `ValidatePrompt` returns.
  """
  @type prompt_validator :: (String.t() -> :ok | {:error, String.t()})

  @typedoc "An option for `validate/3`."
  @type validate_option :: {:prompt_validator, prompt_validator()}

  # Always part of the vocabulary Validate looks for in prompts: a review
  # prompt that says "approve or reject" needs those edges whether or not any
  # other step in the workflow uses them. Go's conventionalOutcomes.
  @conventional_outcomes [Workflow.outcome_approve(), Workflow.outcome_reject()]

  # Introduces the outcome an agent is to finish with: "finish with approve or
  # reject", "finish_step with outcome `stuck`", "set the outcome to escalate".
  # A cue reaches to the end of its sentence. Go's handoffCue, verbatim.
  @handoff_cue ~r/(?i)\b(?:finish(?:_step)?|outcomes?)\b[^.;\n]*/

  @doc """
  Lays the graph out as numbered rows in authoring order.

  `steps` must be in sort order (as the store's `ListSteps` returns them);
  `edges` are matched to them by id and kept in the order given, which for
  `ListEdges` is by source step then outcome. The port of Go's `Preview`.
  """
  @spec preview([Step.t()], [Edge.t()]) :: [PreviewRow.t()]
  def preview(steps, edges) when is_list(steps) and is_list(edges) do
    number = steps |> Enum.with_index(1) |> Map.new(fn {step, n} -> {step.id, n} end)

    steps
    |> Enum.with_index()
    |> Enum.map(fn {step, i} ->
      leaving = Enum.filter(edges, &(&1.from_step_id == step.id))

      listed =
        leaving
        |> Enum.reject(&linear_default?(&1, Map.get(number, &1.to_step_id, 0), i))
        |> Enum.map(&%PreviewEdge{edge: &1, to: Map.get(number, &1.to_step_id, 0)})

      %PreviewRow{number: i + 1, step: step, edges: listed, end?: leaving == []}
    end)
  end

  # done -> the very next step, unbounded: implied by the numbering, so the
  # preview leaves it out.
  defp linear_default?(%Edge{} = edge, to, i) do
    edge.outcome == Workflow.outcome_done() and to == i + 2 and edge.max_iterations == nil
  end

  @doc """
  The preview as plain lines, one per step:

      1. implement
      2. review  [reject -> 1 (max 3)]  [approve -> 3]
      3. gate
      4. ship  [done -> end]

  The port of Go's `PreviewText`.
  """
  @spec preview_text([Step.t()], [Edge.t()]) :: String.t()
  def preview_text(steps, edges) do
    steps
    |> preview(edges)
    |> Enum.map_join("\n", fn row ->
      row
      |> PreviewRow.annotations()
      |> Enum.reduce("#{row.number}. #{row.step.name}", &(&2 <> "  [" <> &1 <> "]"))
    end)
  end

  @doc """
  Reports everything wrong with the graph, in authoring order, empty when it is
  sound. The port of Go's `Validate`.

  It is the authoring-time counterpart of the runner's rules: the runner picks
  every step after the first by edge, so a step no edge leads to never runs; a
  step nothing leaves ends the run, which is only right for the last one; an
  outcome a prompt names has to have an edge or `finish_step` will refuse it; a
  loop-back from an agent step with no `max_iterations` can run forever; and a
  prompt that does not render as a template fails the run at launch.

  That last rule is the one this part of the port does not own. Pass
  `:prompt_validator` to supply it; see the module doc.

  ## Options

    * `:prompt_validator` -- a `t:prompt_validator/0` run over every agent
      step's `prompt_md`. Defaults to a check that passes everything.
  """
  @spec validate([Step.t()], [Edge.t()], [validate_option()]) :: [Problem.t()]
  def validate(steps, edges, opts \\ [])

  def validate([], _edges, _opts), do: [%Problem{msg: "no steps"}]

  def validate([first | _] = steps, edges, opts) when is_list(edges) do
    validate_prompt = Keyword.get(opts, :prompt_validator, &unchecked_prompt/1)

    index = steps |> Enum.with_index() |> Map.new(fn {step, i} -> {step.id, i} end)
    leaving = Enum.group_by(edges, & &1.from_step_id)
    vocabulary = vocabulary(edges)
    reached = reachable(first.id, leaving)
    last = length(steps) - 1

    steps
    |> Enum.with_index()
    |> Enum.flat_map(fn {step, i} ->
      out = Map.get(leaving, step.id, [])
      routes = MapSet.new(out, & &1.outcome)

      unreachable(step, reached) ++
        premature_end(step, out, i, last, steps) ++
        edge_problems(step, out, i, index, steps) ++
        prompt_problems(step, routes, vocabulary, validate_prompt)
    end)
  end

  # The default :prompt_validator: the prompt port is a later part, so until it
  # lands nothing about a prompt's template can be wrong.
  defp unchecked_prompt(_prompt_md), do: :ok

  # Every outcome a prompt could be expected to name: the conventional two,
  # plus every outcome the workflow's edges use. done is never vocabulary --
  # every step may finish with it, so it is not a hand-off the edges have to
  # route.
  defp vocabulary(edges) do
    Enum.reduce(edges, MapSet.new(@conventional_outcomes), fn edge, acc ->
      if edge.outcome == Workflow.outcome_done(),
        do: acc,
        else: MapSet.put(acc, edge.outcome)
    end)
  end

  # Reachability from the first step, the only one entered by position.
  defp reachable(first_id, leaving), do: reach(MapSet.new([first_id]), [first_id], leaving)

  defp reach(reached, [], _leaving), do: reached

  defp reach(reached, [id | frontier], leaving) do
    {reached, frontier} =
      leaving
      |> Map.get(id, [])
      |> Enum.reduce({reached, frontier}, fn edge, {reached, frontier} ->
        if MapSet.member?(reached, edge.to_step_id) do
          {reached, frontier}
        else
          {MapSet.put(reached, edge.to_step_id), frontier ++ [edge.to_step_id]}
        end
      end)

    reach(reached, frontier, leaving)
  end

  defp unreachable(step, reached) do
    if MapSet.member?(reached, step.id),
      do: [],
      else: [problem(step, "unreachable: no edge leads here")]
  end

  defp premature_end(_step, [_ | _], _i, _last, _steps), do: []
  defp premature_end(_step, _out, last, last, _steps), do: []

  defp premature_end(step, _out, i, _last, steps) do
    next = Enum.at(steps, i + 1)
    [problem(step, "no edge leaves it; the run would end here, before #{next.name}")]
  end

  defp edge_problems(step, out, i, index, steps) do
    Enum.flat_map(out, fn edge ->
      case Map.fetch(index, edge.to_step_id) do
        :error ->
          [problem(step, "edge on #{quoted(edge.outcome)} leads to a step that no longer exists")]

        {:ok, to} ->
          loop_back(step, edge, to, i, steps)
      end
    end)
  end

  # A gate's unbounded loop-back is fine: a human decides each time.
  defp loop_back(%Step{kind: :agent} = step, %Edge{max_iterations: nil} = edge, to, i, steps)
       when to <= i do
    target = Enum.at(steps, to)
    [problem(step, "#{quoted(edge.outcome)} loops back to #{target.name} with no max iterations")]
  end

  defp loop_back(_step, _edge, _to, _i, _steps), do: []

  # Gates carry no prompt, so theirs is never checked.
  defp prompt_problems(%Step{kind: :agent} = step, routes, vocabulary, validate_prompt) do
    invalid =
      case validate_prompt.(step.prompt_md) do
        :ok -> []
        {:error, message} -> [problem(step, message)]
      end

    prompt = mask_outcomes(step.prompt_md, Enum.sort(routes))

    mentions =
      vocabulary
      |> Enum.sort()
      |> Enum.filter(&(not MapSet.member?(routes, &1) and mentions_outcome?(prompt, &1)))
      |> Enum.map(&problem(step, "prompt mentions #{quoted(&1)} but no edge routes it"))

    invalid ++ mentions
  end

  defp prompt_problems(_step, _routes, _vocabulary, _validate_prompt), do: []

  # Blanks every whole-word occurrence of the given outcomes in prompt, longest
  # first, so that a routed "wave ready" is not also read as an unrouted
  # "ready". The replacement keeps the prompt's length and breaks word
  # boundaries, so nothing else shifts or matches across it.
  #
  # Go stable-sorts an already lexicographically sorted slice by descending
  # length; sorting by {-length, outcome} lands in the same order without
  # relying on a sort's stability.
  defp mask_outcomes(prompt, outcomes) do
    outcomes
    |> Enum.sort_by(&{-byte_size(&1), &1})
    |> Enum.reduce(prompt, fn outcome, masked ->
      Regex.replace(word(outcome), masked, &String.duplicate("#", byte_size(&1)))
    end)
  end

  # Whether prompt names outcome, case-insensitively and as a whole word, in a
  # hand-off context: quoted or backticked anywhere ("reject", `reject`), or
  # unquoted in the same sentence as a hand-off cue. Prose that happens to use
  # the word -- "wait until the wave is ready", "note it and continue" -- is
  # not a mention: the wave-style workflows route "ready" and "continue" from
  # other steps while their Dispatch prompt uses both as plain English.
  defp mentions_outcome?(prompt, outcome) do
    Regex.match?(quoted_anywhere(outcome), prompt) or
      @handoff_cue
      |> Regex.scan(prompt, capture: :first)
      |> Enum.any?(fn [sentence] -> Regex.match?(word(outcome), sentence) end)
  end

  # Compiled rather than interpolated into a sigil because the outcome is data.
  # Regex.escape/1 is Go's regexp.QuoteMeta, so the result always compiles --
  # Go's code guards the compile error it cannot get either.
  defp word(outcome), do: Regex.compile!("(?i)\\b" <> Regex.escape(outcome) <> "\\b")

  # The opening and closing quotes need not match each other, exactly as in Go:
  # a prompt that says `reject" is still naming the outcome.
  defp quoted_anywhere(outcome) do
    Regex.compile!(~S|(?i)([`"'])| <> Regex.escape(outcome) <> ~S|([`"'])|)
  end

  # Go's %q verb, which quotes an outcome into two of the messages. Tend.Error
  # already owns the port of it, for the same reason: a message whose text is
  # shown to the user has to be the one Go would have printed.
  defp quoted(outcome), do: Tend.Error.quote_go(outcome)

  defp problem(step, msg), do: %Problem{step_id: step.id, step: step.name, msg: msg}
end
