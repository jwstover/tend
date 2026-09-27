defmodule Tend.Workflow.GraphTest do
  @moduledoc """
  A port of `internal/workflow/graph_test.go`: the same fixtures, the same
  cases, the same expected substrings.

  The one case the Go table has and this port cannot run as written is the
  unrenderable template, because `ValidatePrompt` belongs to the prompt part of
  the port. It is covered here through `validate/3`'s `:prompt_validator`, the
  seam the prompt part plugs into, plus a test that pins what the default does
  until then.

  The Go table is faithful but not complete: several of its cases are named for
  a rule they do not actually isolate, so the rule could be wrong and the table
  would still pass. The cases after it -- `"the hand-off cue"` and
  `"rules the Go table names but does not isolate"` -- are not in
  `graph_test.go`; each one is red if the rule it names is mutated, and every
  expectation in them is Go's, taken from running the real `workflow.Validate`
  over the same steps and edges. `"divergences from Go"` pins the two places
  the port knowingly answers differently, so closing the gap is a visible
  change rather than a silent one.
  """

  use ExUnit.Case, async: true

  alias Tend.Workflow.Edge
  alias Tend.Workflow.Graph
  alias Tend.Workflow.Graph.PreviewRow
  alias Tend.Workflow.Graph.Problem
  alias Tend.Workflow.Step

  # The acceptance workflow of the edges editor: implement, review, a manual
  # gate, ship, with a bounded reject loop from review back to implement. Edges
  # are returned the way ListEdges orders them: by source step, then outcome.
  # Go's reviewLoop.
  defp review_loop do
    steps = [
      %Step{
        id: 1,
        name: "implement",
        kind: :agent,
        sort_order: 0,
        prompt_md: "Implement {{.Task.Title}}.{{if .Feedback}} Feedback: {{.Feedback}}{{end}}"
      },
      %Step{
        id: 2,
        name: "review",
        kind: :agent,
        sort_order: 1,
        prompt_md: "Review {{.Input}}; finish with approve or reject."
      },
      %Step{id: 3, name: "gate", kind: :gate, sort_order: 2},
      %Step{id: 4, name: "ship", kind: :agent, sort_order: 3, prompt_md: "Open the PR."}
    ]

    edges = [
      %Edge{id: 10, from_step_id: 1, outcome: "done", to_step_id: 2},
      %Edge{id: 11, from_step_id: 2, outcome: "approve", to_step_id: 3},
      %Edge{id: 12, from_step_id: 2, outcome: "reject", to_step_id: 1, max_iterations: 3},
      %Edge{id: 13, from_step_id: 3, outcome: "approve", to_step_id: 4},
      %Edge{id: 14, from_step_id: 3, outcome: "reject", to_step_id: 1}
    ]

    {steps, edges}
  end

  defp step(steps, i), do: Enum.at(steps, i)

  # Stands in for the prompt part's Tend.Workflow.Prompt validator. Go's
  # ValidatePrompt rejects "{{.Nope}}" because PromptData has no Nope field;
  # this rejects it because that is the one prompt the Go table uses to
  # exercise the case. The message is the text of Go's ErrInvalidPrompt -- the
  # template engine's detail after it is the prompt part's to supply, and the
  # Go test asserts containment, exactly as this one does.
  defp reject_unrenderable(prompt_md) do
    if String.contains?(prompt_md, "{{.Nope}}"),
      do: {:error, "invalid prompt template"},
      else: :ok
  end

  # A port of TestPreviewTextAnnotatesEverythingButTheLinearDefault.
  describe "preview_text/2" do
    test "annotates everything but the linear default" do
      {steps, edges} = review_loop()

      assert Graph.preview_text(steps, edges) ==
               Enum.join(
                 [
                   "1. implement",
                   "2. review  [approve -> 3]  [reject -> 1 (max 3)]",
                   "3. gate  [approve -> 4]  [reject -> 1]",
                   "4. ship  [done -> end]"
                 ],
                 "\n"
               )
    end

    test "of no steps is empty" do
      assert Graph.preview_text([], []) == ""
    end
  end

  # A port of TestPreviewRows.
  describe "preview/2" do
    test "one row per step, in authoring order" do
      {steps, edges} = review_loop()
      rows = Graph.preview(steps, edges)

      assert length(rows) == 4
      assert Enum.map(rows, & &1.number) == [1, 2, 3, 4]
      assert Enum.map(rows, & &1.step.name) == ["implement", "review", "gate", "ship"]
    end

    test "implement's only edge is done -> next: nothing to annotate, and it is not an end" do
      {steps, edges} = review_loop()
      [row | _] = Graph.preview(steps, edges)

      assert row.edges == []
      refute row.end?
      assert PreviewRow.annotations(row) == []
    end

    test "ship has nothing leaving it: an end, annotated as such" do
      {steps, edges} = review_loop()
      row = Graph.preview(steps, edges) |> Enum.at(3)

      assert row.end?
      assert PreviewRow.annotations(row) == ["done -> end"]
    end

    test "a done edge that skips a step, or is bounded, is not the default" do
      {steps, _edges} = review_loop()

      skip = [
        %Edge{from_step_id: 1, outcome: "done", to_step_id: 3},
        %Edge{from_step_id: 3, outcome: "done", to_step_id: 4, max_iterations: 2}
      ]

      rows = Graph.preview(steps, skip)

      assert rows |> Enum.at(0) |> PreviewRow.annotations() == ["done -> 3"]
      assert rows |> Enum.at(2) |> PreviewRow.annotations() == ["done -> 4 (max 2)"]
    end

    test "an edge to a step not in the list renders ? rather than crashing" do
      {steps, _edges} = review_loop()
      rows = Graph.preview(steps, [%Edge{from_step_id: 1, outcome: "done", to_step_id: 99}])

      assert rows |> Enum.at(0) |> PreviewRow.annotations() == ["done -> ?"]
    end
  end

  # A port of TestValidateSoundGraph.
  describe "validate/3 on a sound graph" do
    test "the review loop has no problems" do
      {steps, edges} = review_loop()
      assert Graph.validate(steps, edges) == []
    end

    test "a single step with no edges is the simplest sound workflow" do
      {steps, _edges} = review_loop()
      assert Graph.validate(Enum.take(steps, 1), []) == []
    end
  end

  # A port of TestValidateProblems: the Go table, case for case, in order. Each
  # `want` is a list of substrings, one per expected problem, in the order the
  # problems must come out.
  describe "validate/3 problems" do
    test "the Go table" do
      for {name, steps, edges, want} <- problem_cases() do
        got = Graph.validate(steps, edges, prompt_validator: &reject_unrenderable/1)

        assert length(got) == length(want),
               "#{name}: got #{inspect(Enum.map(got, &Problem.format/1))}, want #{inspect(want)}"

        for {problem, expected} <- Enum.zip(got, want) do
          assert Problem.format(problem) =~ expected, "#{name}: #{Problem.format(problem)}"
        end
      end
    end
  end

  defp problem_cases do
    {steps, edges} = review_loop()

    # A review step whose prompt names no outcome, for the cases about edges
    # alone.
    plain_review = %Step{
      id: 2,
      name: "review",
      kind: :agent,
      sort_order: 1,
      prompt_md: "Review it."
    }

    [
      {"no steps", [], [], ["no steps"]},
      {"no edges at all: every later step is unreachable and the first ends the run",
       [step(steps, 0), plain_review], [],
       [
         "implement: no edge leaves it; the run would end here, before review",
         "review: unreachable: no edge leads here"
       ]},
      {"a step nothing leads to", steps,
       [
         Enum.at(edges, 0),
         %Edge{from_step_id: 2, outcome: "approve", to_step_id: 4},
         Enum.at(edges, 2),
         Enum.at(edges, 3)
       ], ["gate: unreachable"]},
      {"prompt names an outcome the step does not route",
       [
         step(steps, 0),
         %Step{id: 2, name: "review", kind: :agent, prompt_md: "Say APPROVE, or `reject` it."}
       ],
       [
         %Edge{from_step_id: 1, outcome: "done", to_step_id: 2},
         %Edge{from_step_id: 2, outcome: "approve", to_step_id: 1, max_iterations: 2}
       ], [~s(review: prompt mentions "reject" but no edge routes it)]},
      {"an outcome from another step's edges counts as vocabulary too",
       [
         step(steps, 0),
         %Step{id: 2, name: "review", kind: :agent, prompt_md: "finish with retry if flaky"}
       ],
       [
         %Edge{from_step_id: 1, outcome: "retry", to_step_id: 1, max_iterations: 2},
         %Edge{from_step_id: 1, outcome: "done", to_step_id: 2}
       ], [~s(review: prompt mentions "retry" but no edge routes it)]},
      {"a word containing the outcome is not a mention",
       [
         step(steps, 0),
         %Step{
           id: 2,
           name: "ship",
           kind: :agent,
           prompt_md: "The change was approved and rejections are logged."
         }
       ], Enum.take(edges, 1), []},
      # The wave workflow's steps talk about the sub-task done *state* while
      # their own outcomes are "wave ready" and the like. done is never
      # vocabulary: every step may finish with it, so it is not a hand-off the
      # edges have to route.
      {"the sub-task done state is not an outcome: done is never vocabulary",
       [
         step(steps, 0),
         %Step{
           id: 2,
           name: "dispatch",
           kind: :agent,
           permission_mode: "acceptEdits",
           prompt_md:
             "Set the sub-task to done when its PR merges. Finish with `wave ready` or `stuck`."
         }
       ],
       [
         %Edge{from_step_id: 1, outcome: "done", to_step_id: 2},
         %Edge{from_step_id: 2, outcome: "wave ready", to_step_id: 1, max_iterations: 2},
         %Edge{from_step_id: 2, outcome: "stuck", to_step_id: 1, max_iterations: 2}
       ], []},
      # Dispatch's prose uses "ready", "escalate" and "continue" as plain
      # English; the gate and Prepare route them as outcomes. Only a word in a
      # hand-off context is a mention.
      {"wave-style prose: an outcome word outside a hand-off context is not a mention",
       [
         step(steps, 0),
         %Step{
           id: 2,
           name: "dispatch",
           kind: :agent,
           permission_mode: "acceptEdits",
           prompt_md:
             "Wait until the wave is ready. If a sub-task is stuck, do not escalate on your own; note it and continue.\nFinish with `wave ready`, or `stuck` once nothing can proceed."
         },
         %Step{id: 3, name: "gate", kind: :gate}
       ],
       [
         %Edge{from_step_id: 1, outcome: "ready", to_step_id: 2},
         %Edge{from_step_id: 1, outcome: "escalate", to_step_id: 3},
         %Edge{from_step_id: 2, outcome: "wave ready", to_step_id: 1, max_iterations: 2},
         %Edge{from_step_id: 2, outcome: "stuck", to_step_id: 3},
         %Edge{from_step_id: 3, outcome: "continue", to_step_id: 2}
       ], []},
      # "wave ready" is routed; the "ready" inside it is not a second, unrouted
      # mention.
      {"a routed outcome containing a shorter one is not a mention of the shorter",
       [
         step(steps, 0),
         %Step{
           id: 2,
           name: "dispatch",
           kind: :agent,
           permission_mode: "acceptEdits",
           prompt_md: "finish_step with outcome `wave ready`"
         }
       ],
       [
         %Edge{from_step_id: 1, outcome: "ready", to_step_id: 2},
         %Edge{from_step_id: 2, outcome: "wave ready", to_step_id: 1, max_iterations: 2}
       ], []},
      {"an outcome named after a hand-off cue is a mention even unquoted",
       [
         step(steps, 0),
         %Step{
           id: 2,
           name: "dispatch",
           kind: :agent,
           permission_mode: "acceptEdits",
           prompt_md:
             "When nothing can proceed, set the outcome to escalate. Otherwise finish with wave ready."
         },
         %Step{id: 3, name: "gate", kind: :gate}
       ],
       [
         %Edge{from_step_id: 1, outcome: "escalate", to_step_id: 3},
         %Edge{from_step_id: 2, outcome: "wave ready", to_step_id: 1, max_iterations: 2},
         %Edge{from_step_id: 3, outcome: "continue", to_step_id: 2}
       ], [~s(dispatch: prompt mentions "escalate" but no edge routes it)]},
      {"an outcome in quotes is a mention wherever it appears",
       [
         step(steps, 0),
         %Step{
           id: 2,
           name: "review",
           kind: :agent,
           permission_mode: "acceptEdits",
           prompt_md: ~s(If the tests are red, "reject" the change.)
         }
       ],
       [
         %Edge{from_step_id: 1, outcome: "done", to_step_id: 2},
         %Edge{from_step_id: 2, outcome: "approve", to_step_id: 1, max_iterations: 2}
       ], [~s(review: prompt mentions "reject" but no edge routes it)]},
      {"agent loop-back with no max iterations", [step(steps, 0), plain_review],
       [
         %Edge{from_step_id: 1, outcome: "done", to_step_id: 2},
         %Edge{from_step_id: 2, outcome: "reject", to_step_id: 1}
       ], [~s(review: "reject" loops back to implement with no max iterations)]},
      {"a gate's unbounded loop-back is fine: a human decides each time",
       [step(steps, 0), %Step{id: 3, name: "gate", kind: :gate}],
       [
         %Edge{from_step_id: 1, outcome: "done", to_step_id: 3},
         %Edge{from_step_id: 3, outcome: "reject", to_step_id: 1}
       ], []},
      {"template that does not render",
       [%Step{id: 1, name: "implement", kind: :agent, prompt_md: "{{.Nope}}"}], [],
       ["implement: invalid prompt template"]},
      {"gates carry no prompt, so theirs is never checked",
       [%Step{id: 1, name: "gate", kind: :gate, prompt_md: "{{.Nope}} approve"}], [], []},
      {"edge to a missing step", Enum.take(steps, 1),
       [%Edge{from_step_id: 1, outcome: "done", to_step_id: 42}],
       [~s(implement: edge on "done" leads to a step that no longer exists)]}
    ]
  end

  # Every case below runs a two-step graph whose first step carries the prompt
  # under test and whose second routes something back, so the only problems
  # that can come out are the prompt mentions.
  defp agent(id, name, prompt_md),
    do: %Step{id: id, name: name, kind: :agent, prompt_md: prompt_md}

  defp messages(steps, edges),
    do: steps |> Graph.validate(edges) |> Enum.map(&Problem.format/1)

  defp routed_elsewhere(prompt_md, outcomes) do
    steps = [agent(1, "a", prompt_md), agent(2, "b", "Do it.")]

    edges =
      [%Edge{from_step_id: 1, outcome: "done", to_step_id: 2}] ++
        Enum.map(
          outcomes,
          &%Edge{from_step_id: 2, outcome: &1, to_step_id: 1, max_iterations: 1}
        )

    messages(steps, edges)
  end

  # The Go table reaches the cue through "finish with ...", "finish_step with
  # outcome `...`" and "set the outcome to ...", but every one of those cases
  # is also carried by a quoted outcome or by the singular alternative, so each
  # of `outcomes?`, `finish(?:_step)?` and the `;` in `[^.;\n]*` can be cut
  # from the pattern with the table still green. go_parity_test.exs pins the
  # literal on both sides; these pin what it means.
  describe "the hand-off cue" do
    test "the plural cue word introduces an outcome" do
      assert routed_elsewhere("outcomes are escalate and approve", ["escalate"]) == [
               ~s(a: prompt mentions "approve" but no edge routes it),
               ~s(a: prompt mentions "escalate" but no edge routes it)
             ]
    end

    test "finish_step introduces an outcome with no quoting at all" do
      assert routed_elsewhere("finish_step escalate", ["escalate"]) == [
               ~s(a: prompt mentions "escalate" but no edge routes it)
             ]
    end

    test "a semicolon ends a cue, so what follows it is prose again" do
      assert routed_elsewhere("finish now; escalate later", ["escalate"]) == []
    end
  end

  describe "rules the Go table names but does not isolate" do
    # The table's "a routed outcome containing a shorter one" routes only one
    # outcome, so mask_outcomes' longest-first sort is unobservable there:
    # reversing it leaves the table green. Two routed outcomes on one step, one
    # a suffix of the other, make it observable -- masking "y" first would
    # leave the "x" of "x y" visible and reported.
    test "mask_outcomes blanks the longest routed outcome first" do
      steps = [agent(1, "a", "finish with x y"), agent(2, "b", "Do it.")]

      edges = [
        %Edge{from_step_id: 1, outcome: "y", to_step_id: 2},
        %Edge{from_step_id: 1, outcome: "x y", to_step_id: 2},
        %Edge{from_step_id: 2, outcome: "x", to_step_id: 1, max_iterations: 1}
      ]

      assert messages(steps, edges) == []
    end

    # The table's "done is never vocabulary" case names done outside any cue
    # and routes no done edge, so done never enters the vocabulary either way.
    # It has to be named in a hand-off context, by a step that does not route
    # it, while some other step's edge does -- otherwise letting done into the
    # vocabulary changes nothing.
    test "done named in a hand-off cue is still not vocabulary" do
      assert done_is_not_vocabulary("finish with done") == []
    end

    test "done in backticks is not a mention either" do
      assert done_is_not_vocabulary("Say `done` when finished.") == []
    end
  end

  defp done_is_not_vocabulary(prompt_md) do
    steps = [agent(1, "a", prompt_md), agent(2, "b", "Do it.")]

    edges = [
      %Edge{from_step_id: 1, outcome: "other", to_step_id: 2},
      %Edge{from_step_id: 2, outcome: "done", to_step_id: 1, max_iterations: 1}
    ]

    messages(steps, edges)
  end

  describe "divergences from Go" do
    # Go's RE2 folds Unicode under (?i); PCRE without UCP folds ASCII only, so
    # these two graphs are the whole of the gap. See Tend.Workflow.Graph's
    # module doc. Both assert what the port does, not what Go does -- the day
    # the folding is closed, both go red and say so.
    test "a pure-ASCII outcome diverges when the prompt carries a folding rune" do
      # Go folds U+017F LATIN SMALL LETTER LONG S onto "s", reads the quoted
      # "ſtop" as the routed-elsewhere outcome "stop", and reports
      #   a: prompt mentions "stop" but no edge routes it
      assert routed_elsewhere(~s(Say "ſtop" when done.), ["stop"]) == []
    end

    test "an outcome whose own letters are outside ASCII diverges when quoted" do
      # Go reports: a: prompt mentions "café" but no edge routes it
      assert routed_elsewhere(~s(Say "CAFÉ" when done.), ["café"]) == []
    end
  end

  describe "problem order" do
    test "is authoring order, and within a step edges before prompt mentions" do
      {steps, _edges} = review_loop()

      broken = [
        %Step{
          id: 1,
          name: "implement",
          kind: :agent,
          prompt_md: "Implement it; finish with approve."
        },
        %Step{id: 2, name: "review", kind: :agent, prompt_md: "Review it."},
        step(steps, 3)
      ]

      problems =
        Graph.validate(broken, [
          %Edge{from_step_id: 1, outcome: "reject", to_step_id: 1}
        ])

      assert Enum.map(problems, &Problem.format/1) == [
               ~s(implement: "reject" loops back to implement with no max iterations),
               ~s(implement: prompt mentions "approve" but no edge routes it),
               "review: unreachable: no edge leads here",
               "review: no edge leaves it; the run would end here, before ship",
               "ship: unreachable: no edge leads here"
             ]
    end

    test "does not depend on the order the outcomes were authored in" do
      # Two unrouted mentions on one step come out sorted, whichever order the
      # edges that put them in the vocabulary were given in.
      step = %Step{
        id: 2,
        name: "review",
        kind: :agent,
        prompt_md: "finish with escalate, approve or block"
      }

      steps = [%Step{id: 1, name: "implement", kind: :agent, prompt_md: "Do it."}, step]

      messages = fn outcomes ->
        edges =
          [%Edge{from_step_id: 1, outcome: "done", to_step_id: 2}] ++
            Enum.map(
              outcomes,
              &%Edge{from_step_id: 1, outcome: &1, to_step_id: 1, max_iterations: 2}
            )

        steps |> Graph.validate(edges) |> Enum.map(&Problem.format/1)
      end

      assert messages.(["escalate", "block"]) == [
               ~s(review: prompt mentions "approve" but no edge routes it),
               ~s(review: prompt mentions "block" but no edge routes it),
               ~s(review: prompt mentions "escalate" but no edge routes it)
             ]

      assert messages.(["block", "escalate"]) == messages.(["escalate", "block"])
    end

    test "holds past the 32 keys a map keeps in term order" do
      # The case above has a five-entry vocabulary, and Elixir keeps a map of
      # at most 32 keys in term order, so it would pass even if the port
      # iterated the vocabulary rather than sorting it. Sixty outcomes, given
      # shuffled, is past that: the sort in prompt_problems/4 is the only thing
      # putting these in Go's order.
      words = for i <- 0..59, do: "out#{String.pad_leading("#{i}", 2, "0")}"
      shuffled = Enum.shuffle(words)

      want = Enum.map(Enum.sort(words), &~s(a: prompt mentions "#{&1}" but no edge routes it))

      assert routed_elsewhere("finish with " <> Enum.join(shuffled, " or "), shuffled) == want
    end
  end

  describe "the injected prompt check" do
    test "is not run by default, so an unrenderable prompt is not reported yet" do
      # Pins the deferral rather than the behaviour: the prompt part of the
      # port is what makes this case report a problem, and the case above --
      # the same graph with a validator passed -- is what shows the seam works.
      unrenderable = [%Step{id: 1, name: "implement", kind: :agent, prompt_md: "{{.Nope}}"}]

      assert Graph.validate(unrenderable, []) == []
    end

    test "sees every agent step's prompt, and no gate's" do
      {steps, edges} = review_loop()
      test_pid = self()

      seen = fn prompt_md ->
        send(test_pid, {:validated, prompt_md})
        :ok
      end

      assert Graph.validate(steps, edges, prompt_validator: seen) == []

      for %Step{kind: :agent} = step <- steps do
        assert_received {:validated, prompt_md}
        assert prompt_md == step.prompt_md
      end

      refute_received {:validated, _}
    end

    test "its message becomes the problem's, verbatim" do
      steps = [%Step{id: 1, name: "implement", kind: :agent, prompt_md: "anything"}]

      fail = fn _prompt ->
        {:error, "invalid prompt template: template: prompt:1:2: no such field"}
      end

      assert [problem] = Graph.validate(steps, [], prompt_validator: fail)

      assert Problem.format(problem) ==
               "implement: invalid prompt template: template: prompt:1:2: no such field"
    end
  end

  describe "an outcome in a message" do
    test "is quoted the way Go's %q verb quotes it" do
      # inspect/1 would spell the same outcome "a\"b\\c" differently only in
      # the cases Tend.Error.quote_go/1 exists for; using the verb here is what
      # keeps a problem's text the one Go would have printed.
      steps = [%Step{id: 1, name: "implement", kind: :agent, prompt_md: "Do it."}]
      edges = [%Edge{from_step_id: 1, outcome: ~S(a"b\c), to_step_id: 42}]

      assert [problem] = Graph.validate(steps, edges)

      assert Problem.format(problem) ==
               ~S(implement: edge on "a\"b\\c" leads to a step that no longer exists)
    end
  end

  describe "Problem.format/1" do
    test "reads 'step: message'" do
      assert Problem.format(%Problem{step_id: 2, step: "review", msg: "unreachable"}) ==
               "review: unreachable"
    end

    test "is the bare message for a problem about no step in particular" do
      assert Problem.format(%Problem{msg: "no steps"}) == "no steps"
      assert Graph.validate([], []) == [%Problem{step_id: 0, step: "", msg: "no steps"}]
    end
  end
end
