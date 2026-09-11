defmodule Spike4.Markdown do
  @moduledoc false

  alias BackBreeze.TextSpan

  @body "fixtures/body-206.md"
  @glamour "tmp/glamour-80.ans"

  def run(width) do
    body = File.read!(@body)
    File.mkdir_p!("tmp")

    # 1. The renderer on its own: spans back to SGR text.
    direct = body |> Breeze.Markdown.render(width) |> spans_to_ansi()
    File.write!("tmp/breeze-direct-#{width}.ans", direct)

    # 2. The block under Breeze.Test, as the detail pane would show it. A
    #    tall terminal so the scroll box shows the whole body.
    session =
      Breeze.Test.start!(Spike4.MdView,
        size: {width, 400},
        start_opts: [body: body, width: width]
      )

    frame = Breeze.Test.render!(session)
    Breeze.Test.stop(session)
    File.write!("tmp/breeze-frame-#{width}.ans", frame)
    frame_text = frame |> BackBreeze.Utils.strip_escape_chars() |> trim_frame()
    File.write!("tmp/breeze-frame-#{width}.txt", frame_text)

    glamour =
      case File.read(@glamour) do
        {:ok, g} -> g
        _ -> ""
      end

    glamour_text = strip_all(glamour)
    File.write!("tmp/glamour-#{width}.txt", glamour_text)
    direct_text = BackBreeze.Utils.strip_escape_chars(direct)

    IO.puts("Breeze.Markdown.render/2 at width #{width}: #{count_lines(direct_text)} lines")
    IO.puts("<.markdown> block frame at #{width}x400: #{count_lines(frame_text)} non-blank lines")
    IO.puts("glamour at width #{width}: #{count_lines(glamour_text)} lines")
    IO.puts("")

    checks(body, direct, direct_text, frame, frame_text, glamour, glamour_text, width)

    IO.puts("")
    IO.puts("--- Breeze.Markdown, first 60 lines (styling stripped) ---")
    direct_text |> String.split("\n") |> Enum.take(60) |> Enum.each(&IO.puts("| " <> &1))
  end

  # Each check names a construct in body-206.md and reports what each
  # renderer did with it. PASS means Breeze's output is acceptable next
  # to glamour's for that construct; FAIL means a reader would lose
  # information or structure.
  defp checks(body, direct, direct_text, _frame, frame_text, glamour, glamour_text, width) do
    result("headings keep their level", fn ->
      # glamour: "## Plan" and "### Headline" prefixes survive, styled per level.
      # Breeze: every heading line gets the same black-on-yellow bar.
      h2 = line_containing(direct_text, "Plan (Plan workflow")
      h3 = line_containing(direct_text, "Headline")
      g2 = line_containing(glamour_text, "Plan (Plan workflow")
      g3 = line_containing(glamour_text, "Headline")

      styles = heading_styles(direct)

      {String.starts_with?(String.trim(h2 || ""), "##") and
         String.starts_with?(String.trim(h3 || ""), "###") and
         map_size(styles) >= 2,
       "breeze h2=#{inspect(h2)} h3=#{inspect(h3)} distinct heading styles=#{map_size(styles)} #{inspect(Map.keys(styles))}; glamour h2=#{inspect(g2)} h3=#{inspect(g3)}"}
    end)

    result("table renders as a table", fn ->
      # The stack table: 8 rows, 3 columns. glamour draws column rules.
      b = line_containing(direct_text, "Fuzzy filter")
      g = line_containing(glamour_text, "Fuzzy filter")
      table_rows = direct_text |> String.split("\n") |> Enum.count(&String.contains?(&1, "│"))
      g_rows = glamour_text |> String.split("\n") |> Enum.count(&String.contains?(&1, "│"))

      {table_rows >= 8,
       "breeze lines with │: #{table_rows}, row 'Fuzzy filter' -> #{inspect(b)}; glamour lines with │: #{g_rows}, -> #{inspect(g)}"}
    end)

    result("ordered list keeps numbers and one item per paragraph", fn ->
      # "1. **Terminal handoff.** ..." through "4. **Markdown and ANSI width.** ..."
      lines = String.split(direct_text, "\n")
      numbered = Enum.count(lines, &Regex.match?(~r/^\s*[1-4]\. /, &1))

      g_numbered =
        glamour_text |> String.split("\n") |> Enum.count(&Regex.match?(~r/^\s*[1-4]\. /, &1))

      merged = line_containing(direct_text, "stop here. 2.")

      {numbered >= 4 and merged == nil,
       "breeze numbered lines=#{numbered} merged-items-line=#{inspect(merged)}; glamour numbered lines=#{g_numbered}"}
    end)

    result("bullet list items are separate lines", fn ->
      bullets =
        direct_text
        |> String.split("\n")
        |> Enum.count(&String.starts_with?(String.trim_leading(&1), "•"))

      g_bullets =
        glamour_text
        |> String.split("\n")
        |> Enum.count(&String.starts_with?(String.trim_leading(&1), "•"))

      src = body |> String.split("\n") |> Enum.count(&String.starts_with?(&1, "- "))

      {bullets == src,
       "source '- ' items=#{src}, breeze bullets=#{bullets}, glamour bullets=#{g_bullets}"}
    end)

    result("fenced code block", fn ->
      # body-206 has no fence; test the construct on its own.
      md = "Before\n\n```sh\ncd spikes/04\nmix deps.get\n```\n\nAfter"

      out =
        md
        |> Breeze.Markdown.render(width)
        |> spans_to_ansi()
        |> BackBreeze.Utils.strip_escape_chars()

      ok = String.contains?(out, "    cd spikes/04\n    mix deps.get")
      {ok, inspect(out)}
    end)

    result("inline code is styled", fn ->
      spans = Breeze.Markdown.render("uses `internal/tui` here", width)
      code = Enum.find(spans, &(&1.text == "internal/tui"))
      {code != nil and code.style != %{}, inspect(spans)}
    end)

    result("bold is styled", fn ->
      spans = Breeze.Markdown.render("a **strong** word", width)
      b = Enum.find(spans, &(&1.text == "strong"))
      {b != nil and b.style[:bold] == true, inspect(spans)}
    end)

    result("italic is styled", fn ->
      spans = Breeze.Markdown.render("an *em* word and _this_", width)
      texts = Enum.map(spans, & &1.text)
      {Enum.any?(spans, &(&1.text in ["em", "this"] and &1.style != %{})), inspect(texts)}
    end)

    result("links keep text and target", fn ->
      spans = Breeze.Markdown.render("see [Breeze](https://hex.pm/packages/breeze) now", width)
      text = spans |> Enum.map(& &1.text) |> Enum.join()
      g = line_containing(glamour_text, "hex.pm/packages/breeze")

      {String.contains?(text, "Breeze") and String.contains?(text, "hex.pm/packages/breeze"),
       "breeze=#{inspect(text)}; glamour bare URL line=#{inspect(g)}; glamour emits OSC 8: #{String.contains?(glamour, "\e]8;")}"}
    end)

    result("bare URL survives", fn ->
      l = line_containing(direct_text, "https://hex.pm/packages/breeze")
      {l != nil, inspect(l)}
    end)

    result("nested list (2-space indent) keeps nesting", fn ->
      md = "- top\n  - nested\n- top two"

      out =
        md
        |> Breeze.Markdown.render(width)
        |> spans_to_ansi()
        |> BackBreeze.Utils.strip_escape_chars()

      {String.contains?(out, "\n  • nested") or String.contains?(out, "\n    • nested"),
       inspect(out)}
    end)

    result("blockquote", fn ->
      md = "> quoted line"

      out =
        md
        |> Breeze.Markdown.render(width)
        |> spans_to_ansi()
        |> BackBreeze.Utils.strip_escape_chars()

      {not String.starts_with?(out, "> quoted"), inspect(out)}
    end)

    result("horizontal rule", fn ->
      md = "a\n\n---\n\nb"

      out =
        md
        |> Breeze.Markdown.render(width)
        |> spans_to_ansi()
        |> BackBreeze.Utils.strip_escape_chars()

      {not String.contains?(out, "---"), inspect(out)}
    end)

    result("hard line break / trailing double space", fn ->
      md = "line one  \nline two"

      out =
        md
        |> Breeze.Markdown.render(width)
        |> spans_to_ansi()
        |> BackBreeze.Utils.strip_escape_chars()

      {String.contains?(out, "line one\nline two"), inspect(out)}
    end)

    result("no line wider than the pane", fn ->
      widths = direct_text |> String.split("\n") |> Enum.map(&BackBreeze.Utils.string_length/1)
      over = Enum.filter(widths, &(&1 > width))
      {over == [], "max=#{Enum.max(widths)} over=#{length(over)}"}
    end)

    result("block frame agrees with the direct render", fn ->
      # The <.markdown> block should show the same text the renderer made.
      d =
        direct_text
        |> String.split("\n")
        |> Enum.map(&String.trim_trailing/1)
        |> Enum.reject(&(&1 == ""))

      f =
        frame_text
        |> String.split("\n")
        |> Enum.map(&String.trim_trailing/1)
        |> Enum.reject(&(&1 == ""))

      {d == f,
       "direct=#{length(d)} lines, frame=#{length(f)} lines, first diff=#{inspect(first_diff(d, f))}"}
    end)
  end

  def ast do
    body = File.read!(@body)
    {:ok, ast, []} = EarmarkParser.as_ast(body, gfm_tables: true)

    IO.puts("earmark_parser AST for body-206.md: #{length(ast)} top-level nodes")

    ast
    |> Enum.map(fn
      {tag, attrs, children, _meta} -> {tag, attrs, children}
      other -> other
    end)
    |> Enum.each(fn
      {"table", _, rows} -> IO.puts("  table: #{length(rows)} children (thead/tbody)")
      {"ol", _, items} -> IO.puts("  ol: #{length(items)} items")
      {"ul", _, items} -> IO.puts("  ul: #{length(items)} items")
      {tag, _, children} -> IO.puts("  #{tag}: #{summarize(children)}")
      other -> IO.puts("  #{inspect(other)}")
    end)
  end

  defp summarize([text | _]) when is_binary(text), do: inspect(String.slice(text, 0, 50))
  defp summarize([{tag, _, _, _} | _]), do: "<#{tag}> ..."
  defp summarize(other), do: inspect(other, limit: 3)

  # -- helpers -------------------------------------------------------------

  defp result(name, fun) do
    {ok, detail} = fun.()
    IO.puts("#{if ok, do: "PASS", else: "FAIL"}  #{name}")
    IO.puts("      #{detail}")
  end

  defp line_containing(text, needle) do
    text |> String.split("\n") |> Enum.find(&String.contains?(&1, needle))
  end

  defp heading_styles(ansi) do
    # Breeze.Markdown marks a heading with one fixed SGR; count the distinct
    # SGR sequences that open a line whose text came from a heading.
    ~r/^(\e\[[0-9;]*m)/m
    |> Regex.scan(ansi)
    |> Enum.map(fn [_, sgr] -> sgr end)
    |> Enum.filter(&String.contains?(&1, "4"))
    |> Enum.frequencies()
  end

  defp first_diff(a, b) do
    Enum.zip(a, b) |> Enum.find(fn {x, y} -> x != y end)
  end

  defp count_lines(text) do
    text |> String.split("\n") |> Enum.reject(&(String.trim(&1) == "")) |> length()
  end

  defp trim_frame(text) do
    text
    |> String.split("\n")
    |> Enum.map(&String.trim_trailing/1)
    |> Enum.reverse()
    |> Enum.drop_while(&(&1 == ""))
    |> Enum.reverse()
    |> Enum.join("\n")
  end

  # glamour output carries OSC 8 hyperlinks (ESC ] 8 ; ... BEL) which
  # BackBreeze.Utils.strip_escape_chars does not understand; strip those
  # first so the comparison is on text.
  defp strip_all(s) do
    s
    |> String.replace(~r/\e\]8;[^\a]*\a/, "")
    |> BackBreeze.Utils.strip_escape_chars()
  end

  defp spans_to_ansi(spans) do
    Enum.map_join(spans, fn %TextSpan{text: text, style: style} ->
      sgr =
        Enum.flat_map(style, fn
          {:foreground_color, c} when is_integer(c) -> ["3#{c}"]
          {:background_color, c} when is_integer(c) -> ["4#{c}"]
          {:bold, true} -> ["1"]
          _ -> []
        end)

      case sgr do
        [] -> text
        codes -> "\e[" <> Enum.join(codes, ";") <> "m" <> text <> "\e[0m"
      end
    end)
  end
end
