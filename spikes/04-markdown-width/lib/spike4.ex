defmodule Spike4 do
  @moduledoc """
  Spike 4: markdown fidelity (Breeze.Markdown vs glamour) and ANSI width
  (BackBreeze vs x/ansi). Throwaway, per task #207.

      mix run -e 'Spike4.main(System.argv())' -- markdown [WIDTH]
      mix run -e 'Spike4.main(System.argv())' -- width [tmp/go-widths.json]
      mix run -e 'Spike4.main(System.argv())' -- ast

  `markdown` renders fixtures/body-206.md through Breeze.Markdown directly
  and through the `<.markdown>` block under Breeze.Test, writes both to
  tmp/, and prints a construct-by-construct comparison with the glamour
  render gocheck produced. `width` runs BackBreeze's width, truncate and
  wrap primitives over gocheck's fixtures and prints PASS/FAIL per check.
  `ast` shows what earmark_parser makes of the same body, for the fail
  path's cost estimate.
  """

  def main(["markdown" | rest]) do
    width = rest |> List.first("80") |> String.to_integer()
    Spike4.Markdown.run(width)
  end

  def main(["width" | rest]) do
    path = List.first(rest, "tmp/go-widths.json")
    Spike4.Width.run(path)
  end

  def main(["ast" | _]), do: Spike4.Markdown.ast()

  def main(["gwidth" | rest]) do
    path = List.first(rest, "tmp/go-widths.json")
    Spike4.GWidth.run(path)
  end

  def main(_) do
    IO.puts(@moduledoc)
    System.halt(2)
  end
end
