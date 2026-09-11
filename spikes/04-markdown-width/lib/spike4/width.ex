defmodule Spike4.Width do
  @moduledoc """
  BackBreeze's width primitives against x/ansi, case by case.

  Go side (gocheck): `ansi.StringWidth`, `ansi.Truncate(s, w, tail)`,
  `ansi.Wrap(s, w, "")`, `ansi.Strip`.

  Elixir side: `BackBreeze.Utils.string_length/1` (the width every Breeze
  box measures with), `BackBreeze.String.truncate/2`, `BackBreeze.String.reflow/3`
  (the wrap a binary box content goes through), and `BackBreeze.TextLayout.prepare/4`
  on spans (the path styled `TextSpan` content goes through).
  """

  alias BackBreeze.{String, TextLayout, TextSpan, Ucwidth, Utils}

  def run(path) do
    cases = path |> File.read!() |> Jason.decode!()
    IO.puts("#{length(cases)} fixtures from #{path}\n")

    tallies =
      cases
      |> Enum.map(&check_case/1)
      |> List.flatten()
      |> Enum.group_by(fn {check, _name, ok, _d} -> {check, ok} end)
      |> Enum.map(fn {{check, ok}, list} -> {check, ok, length(list)} end)
      |> Enum.sort()

    IO.puts("\n--- tally ---")

    tallies
    |> Enum.group_by(fn {check, _, _} -> check end)
    |> Enum.sort()
    |> Enum.each(fn {check, rows} ->
      pass = rows |> Enum.filter(fn {_, ok, _} -> ok end) |> Enum.map(&elem(&1, 2)) |> Enum.sum()
      fail = rows |> Enum.reject(fn {_, ok, _} -> ok end) |> Enum.map(&elem(&1, 2)) |> Enum.sum()
      IO.puts(Elixir.String.pad_trailing(check, 34) <> "pass #{pass}  fail #{fail}")
    end)
  end

  defp check_case(%{"name" => name, "input" => input, "strip" => strip, "width" => go_w} = c) do
    IO.puts("== #{name}  #{inspect(input)}")

    checks =
      [
        # 1. Width of the styled string, as Breeze measures box content.
        check("width/string_length(styled)", name, Utils.string_length(input), go_w),
        # 2. Width the span layout path computes: per-grapheme Ucwidth.
        check("width/grapheme_sum(stripped)", name, grapheme_sum(strip), go_w),
        # 3. Strip.
        check("strip", name, Utils.strip_escape_chars(input), strip)
      ] ++
        Enum.flat_map(c["truncate"], fn %{"w" => w, "tail" => tail, "out" => go_out} ->
          go_text =
            Utils.strip_escape_chars(Elixir.String.replace(go_out, ~r/\e\]8;[^\e]*\e\\/, ""))

          if tail == "" do
            [
              # 4. truncate on the styled string: what a Breeze box does with
              #    overflow-hidden on an SGR-carrying binary.
              check(
                "truncate/styled w=#{w}",
                name,
                Utils.strip_escape_chars(String.truncate(input, w)),
                go_text
              ),
              # 5. truncate on the stripped string: the pure width question.
              check("truncate/stripped w=#{w}", name, String.truncate(strip, w), go_text)
            ]
          else
            # 6. Truncate with an ellipsis tail, emulated the way x/ansi does it.
            [check("truncate/tail w=#{w}", name, truncate_tail(strip, w, tail), go_text)]
          end
        end) ++
        Enum.flat_map(c["wrap"], fn %{"w" => w, "out" => go_out} ->
          go_lines = go_out |> strip_all() |> Elixir.String.split("\n")
          reflow = String.reflow(input, w)
          reflow_lines = reflow |> Utils.strip_escape_chars() |> Elixir.String.split("\n")
          span_lines = span_wrap(strip, w)

          [
            # 7. reflow line-for-line against ansi.Wrap.
            check("wrap/reflow==x/ansi w=#{w}", name, reflow_lines, go_lines),
            # 8. reflow invariants: fits, loses nothing.
            check(
              "wrap/reflow fits w=#{w}",
              name,
              Enum.max(Enum.map(reflow_lines, &Utils.string_length/1)) <= w,
              true
            ),
            check(
              "wrap/reflow keeps text w=#{w}",
              name,
              squash(Enum.join(reflow_lines)),
              squash(strip)
            ),
            # 9. span layout (grapheme break) invariants.
            check(
              "wrap/spans fit w=#{w}",
              name,
              Enum.max(Enum.map(span_lines, &grapheme_sum/1)) <= w,
              true
            ),
            check(
              "wrap/spans keep text w=#{w}",
              name,
              squash(Enum.join(span_lines)),
              squash(strip)
            )
          ]
        end)

    Enum.each(checks, fn {check, _name, ok, detail} ->
      unless ok, do: IO.puts("  FAIL #{check}  #{detail}")
    end)

    n_fail = Enum.count(checks, fn {_, _, ok, _} -> not ok end)
    IO.puts("  #{length(checks) - n_fail}/#{length(checks)} pass")
    checks
  end

  defp check(label, name, got, want) do
    {label, name, got == want, "elixir=#{inspect(got)} go=#{inspect(want)}"}
  end

  defp grapheme_sum(s),
    do: s |> Elixir.String.graphemes() |> Enum.map(&Ucwidth.width/1) |> Enum.sum()

  # x/ansi: if width(s) <= w, s; else truncate to w - width(tail) and append tail.
  defp truncate_tail(s, w, tail) do
    if Utils.string_length(s) <= w do
      s
    else
      String.truncate(s, w - Utils.string_length(tail)) <> tail
    end
  end

  defp span_wrap(s, w) do
    %{lines: lines} = TextLayout.prepare([TextSpan.new(s)], w, :wrap, 0)

    lines
    |> Tuple.to_list()
    |> Enum.map(fn segs -> Enum.map_join(segs, fn {t, _} -> t end) end)
  end

  defp squash(s), do: s |> Elixir.String.replace(~r/\s+/u, "") |> Elixir.String.replace("­", "")

  defp strip_all(s) do
    s
    |> Elixir.String.replace(~r/\e\]8;[^\e]*\e\\/, "")
    |> Utils.strip_escape_chars()
  end
end
