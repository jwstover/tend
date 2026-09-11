defmodule Spike4.GWidth do
  @moduledoc """
  The fail-path estimate for width, made concrete: a grapheme-cluster width
  rule on top of `BackBreeze.Ucwidth`, in the shape a vendored patch or a
  BackBreeze PR would take, run against gocheck's reference.

  Rule: a cluster is 0 wide if it is a control (tab, ZWSP alone, soft
  hyphen); 2 wide if it holds U+FE0F (emoji presentation), a keycap
  U+20E3, or is a regional-indicator pair (flag); otherwise the width of
  its first codepoint (BackBreeze's current answer, which already covers
  ZWJ sequences, skin tones and combining marks because the first
  codepoint carries the width).

  Also exercises `BackBreeze.TextLayout.prepare/4` with overflow :hidden,
  the path a fixed-width row cell goes through.
  """

  alias BackBreeze.{TextLayout, TextSpan, Ucwidth}

  def width(s) when is_binary(s) do
    s |> String.graphemes() |> Enum.map(&cluster_width/1) |> Enum.sum()
  end

  def cluster_width(g) do
    cps = String.to_charlist(g)

    cond do
      cps == [?\t] -> 0
      Enum.any?(cps, &(&1 == 0xFE0F or &1 == 0x20E3)) -> 2
      match?([a, b] when a in 0x1F1E6..0x1F1FF and b in 0x1F1E6..0x1F1FF, cps) -> 2
      true -> Ucwidth.width(g)
    end
  end

  def truncate(s, w) do
    {out, _} =
      s
      |> String.graphemes()
      |> Enum.reduce_while({"", 0}, fn g, {acc, cur} ->
        gw = cluster_width(g)
        if cur + gw > w, do: {:halt, {acc, cur}}, else: {:cont, {acc <> g, cur + gw}}
      end)

    out
  end

  def run(path) do
    cases = path |> File.read!() |> Jason.decode!()

    results =
      Enum.flat_map(cases, fn %{"name" => name, "strip" => strip, "width" => go_w} = c ->
        w_ok = width(strip) == go_w

        t_ok =
          Enum.all?(c["truncate"], fn
            %{"tail" => "", "w" => w, "out" => out} ->
              # x/ansi keeps the OSC 8 wrapper around truncated link text;
              # drop it before BackBreeze's SGR-only stripper sees it.
              go =
                out
                |> String.replace(~r/\e\]8;[^\e]*\e\\/, "")
                |> BackBreeze.Utils.strip_escape_chars()

              truncate(strip, w) == go

            _ ->
              true
          end)

        # Breeze's own span truncate (overflow :hidden, height 0) for a
        # one-line row cell, checked with its own width function: the cell
        # must never exceed the box.
        hidden_ok =
          Enum.all?([1, 2, 3, 4, 6, 8], fn w ->
            %{lines: lines} = TextLayout.prepare([TextSpan.new(strip)], w, :hidden, 0)

            case Tuple.to_list(lines) do
              [] -> true
              [line | _] -> line |> Enum.map_join(fn {t, _} -> t end) |> then(&(width(&1) <= w))
            end
          end)

        unless w_ok, do: IO.puts("  width mismatch #{name}: patched=#{width(strip)} go=#{go_w}")
        unless t_ok, do: IO.puts("  truncate mismatch #{name}")
        unless hidden_ok, do: IO.puts("  span truncate overflow #{name}")
        [{:width, w_ok}, {:truncate, t_ok}, {:span_hidden, hidden_ok}]
      end)

    for kind <- [:width, :truncate, :span_hidden] do
      rows = for {^kind, ok} <- results, do: ok

      IO.puts(
        "patched #{kind}: pass #{Enum.count(rows, & &1)}  fail #{Enum.count(rows, &(not &1))}  of #{length(rows)}"
      )
    end
  end
end
