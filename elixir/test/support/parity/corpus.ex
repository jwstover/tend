defmodule Tend.Template.Parity.Corpus do
  @moduledoc """
  Where the parity cases come from.

  Two sources, and they are the two the sub-task names:

    * **the repo's test fixtures** -- every Go string literal in a `*_test.go`
      file that contains `{{`. That is where a `prompt_md` lives in the Go
      tree: `prompt_test.go`'s `readySetTpl` and the render and validate
      tables, plus whatever the store, TUI and MCP tests write into a
      `prompt_md` column. Scanning for them beats listing them, because a Go
      test that adds a template is exactly the change this corpus must not
      miss, and `Tend.Template.Parity` says so out loud when one appears that
      the recorded corpus has no Go output for.
    * **a real user `tend.db`** -- every `prompt_md` row in `workflow_steps`.
      The file is copied to a temporary directory and opened read-only, twice
      over, so a parity run can never write to the database a user is working
      in.

  Each template is paired with every data set in `Tend.Template.Parity.Data`,
  so a prompt is rendered against an empty `PromptData` and a populated one
  and both arms of the usual `{{if .Feedback}}` are walked.

  On top of those two sources there is `extra_templates/0`: a short list of
  constructs a stored `prompt_md` can be written in that no `*_test.go` in
  this repo happens to contain. They are kept here rather than added to a Go
  test file because the Go tree is the one that ships and its test suite is
  not this port's to grow. `Tend.Template.Parity.Data` says what the corpus
  can and cannot reach even with them.
  """

  alias Tend.Template.Parity.Data

  @golden Path.expand("../../fixtures/parity/repo_prompts.jsonl", __DIR__)

  @doc "The checked-in file of Go outputs for the repo's fixture corpus."
  @spec golden_path() :: binary()
  def golden_path, do: @golden

  # Templates the repo's Go tests do not write. `range` with an `{{else}}` arm
  # is the whole of it: 11 of the repo's templates use `range` and not one of
  # them has an `{{else}}`, so the arm Go takes for an empty sequence was
  # never rendered by this corpus at all -- including the integer `range` Go
  # 1.22 added, which `{{range .Iteration}}` reaches from a stored prompt.
  @extra_templates [
    "{{range .Outcomes}}- {{.}}\n{{else}}none\n{{end}}",
    "{{range $i, $s := .Subtasks}}{{$i}}:{{$s.Title}}\n{{else}}no subtasks\n{{end}}",
    "{{range .Iteration}}.{{else}}not started{{end}}"
  ]

  @doc """
  The templates in `repo_cases/0` that come from this module rather than from
  a Go test file. See the moduledoc for why they are here.
  """
  @spec extra_templates() :: [binary()]
  def extra_templates, do: @extra_templates

  @doc """
  Every template the repo has to offer, paired with every data set: the ones
  scanned out of its Go test fixtures, plus `extra_templates/0`.

  Ordered by template then data set, so the golden file has a stable diff.
  """
  @spec repo_cases() :: [map()]
  def repo_cases do
    from_go_tests =
      repo_root()
      |> Path.join("**/*_test.go")
      |> Path.wildcard()
      |> Enum.sort()
      |> Enum.flat_map(&templates_in/1)

    (from_go_tests ++ @extra_templates)
    |> Enum.uniq()
    |> Enum.sort()
    |> Enum.flat_map(&cases_for(label(&1), &1))
  end

  defp label(template), do: if(template in @extra_templates, do: "extra", else: "repo")

  @doc """
  Every `prompt_md` row in the `tend.db` at `path`, paired with every data
  set.

  The database is copied first -- along with its `-wal` side file, or a copy
  would miss everything written since the last checkpoint -- and the copy is
  opened `-readonly`. The original is never opened at all.

  The `-shm` file is deliberately *not* copied: it is a rebuildable index over
  the `-wal`, and a copy of it taken while `tend` is writing can be a snapshot
  of a different instant than the two files it indexes. SQLite recreates it.

  Returns `{:ok, cases, row_count}` or `{:error, reason}`.
  """
  @spec db_cases(binary()) :: {:ok, [map()], non_neg_integer()} | {:error, binary()}
  def db_cases(path) do
    cond do
      not File.exists?(path) -> {:error, "no database at #{path}"}
      System.find_executable("sqlite3") == nil -> {:error, "sqlite3 is not on the PATH"}
      true -> read_db(path)
    end
  end

  defp read_db(path) do
    dir = Path.join(System.tmp_dir!(), "tend-parity-db-#{System.unique_integer([:positive])}")

    try do
      File.mkdir_p!(dir)
      copy = Path.join(dir, "tend.db")
      File.cp!(path, copy)

      if File.exists?(path <> "-wal"), do: File.cp!(path <> "-wal", copy <> "-wal")

      case System.cmd("sqlite3", ["-readonly", "-json", copy, query()], stderr_to_stdout: true) do
        {"", 0} ->
          {:ok, [], 0}

        {output, 0} ->
          rows = JSON.decode!(output)
          {:ok, Enum.flat_map(rows, &db_case(&1)), length(rows)}

        {output, status} ->
          {:error, "sqlite3 exited #{status}: #{String.trim(output)}"}
      end
    after
      File.rm_rf(dir)
    end
  end

  defp query do
    """
    SELECT s.id AS id, w.name AS workflow, s.name AS step, s.prompt_md AS prompt_md
    FROM workflow_steps s JOIN workflows w ON w.id = s.workflow_id
    ORDER BY s.id
    """
  end

  defp db_case(row) do
    cases_for("db/#{row["id"]} #{row["workflow"]}/#{row["step"]}", row["prompt_md"])
  end

  defp cases_for(label, template) do
    Enum.map(Data.names(), fn data ->
      %{name: "#{label} [#{data}]", template: template, data: data}
    end)
  end

  @doc "The golden file, as a map of `{template, data set}` to Go's result."
  @spec golden() :: %{optional({binary(), binary()}) => map()}
  def golden do
    case File.read(golden_path()) do
      {:ok, contents} ->
        contents
        |> String.split("\n", trim: true)
        |> Enum.map(&JSON.decode!/1)
        |> Map.new(&{{&1["template"], &1["data"]}, &1})

      {:error, _reason} ->
        %{}
    end
  end

  @doc """
  Writes `results` -- whatever `Tend.Template.Parity.Go.render/1` returned --
  to the golden file, one JSON object per line, ordered for a stable diff.
  """
  @spec write_golden([map()]) :: :ok
  def write_golden(results) do
    lines =
      results
      |> Enum.sort_by(&{&1["template"], &1["data"]})
      |> Enum.map(&(JSON.encode!(Map.delete(&1, "name")) <> "\n"))

    File.mkdir_p!(Path.dirname(golden_path()))
    File.write!(golden_path(), lines)
  end

  @doc "The repository root, the directory holding `go.mod`."
  @spec repo_root() :: binary()
  def repo_root do
    from_cwd = Path.expand("..", File.cwd!())

    if File.exists?(Path.join(from_cwd, "go.mod")) do
      from_cwd
    else
      Path.expand("../../../..", __DIR__)
    end
  end

  ## Pulling template literals out of Go source

  # Go string literals are scanned rather than matched with a regular
  # expression because a template is full of quotes and braces: the readiness
  # prompt is written `"{{if and (ne .State \"done\") ...}}"`, and a regex
  # that stops at the first `"` would cut it in half. Adjacent literals joined
  # by `+` are concatenated, which is how the longer prompts in the Go tests
  # are written, and comments are skipped so a commented-out template is not
  # mistaken for a live one.
  defp templates_in(file) do
    file
    |> File.read!()
    |> literals()
    |> Enum.filter(&String.contains?(&1, "{{"))
  end

  defp literals(source), do: scan(source, [], nil)

  # Nothing left: whatever literal was being accumulated is finished.
  defp scan(<<>>, acc, pending), do: Enum.reverse(flush(pending, acc))

  defp scan(<<"//", rest::binary>>, acc, pending) do
    rest = skip_to(rest, "\n")
    scan(rest, flush(pending, acc), nil)
  end

  defp scan(<<"/*", rest::binary>>, acc, pending) do
    rest = skip_to(rest, "*/")
    scan(rest, flush(pending, acc), nil)
  end

  # A rune literal can hold a quote (`'"'`), so it has to be stepped over.
  defp scan(<<?', rest::binary>>, acc, pending) do
    rest = skip_rune(rest)
    scan(rest, flush(pending, acc), nil)
  end

  defp scan(<<?", rest::binary>>, acc, pending) do
    {value, rest} = interpreted(rest, [])
    scan(rest, acc, join(pending, value))
  end

  defp scan(<<?`, rest::binary>>, acc, pending) do
    {value, rest} = raw(rest, [])
    scan(rest, acc, join(pending, value))
  end

  # `+` and whitespace between two literals continue a concatenation; anything
  # else ends it.
  defp scan(<<char, rest::binary>>, acc, pending) when char in ~c"+ \t\r\n",
    do: scan(rest, acc, pending)

  defp scan(<<_char, rest::binary>>, acc, pending), do: scan(rest, flush(pending, acc), nil)

  defp join(nil, value), do: value
  defp join(pending, value), do: pending <> value

  defp flush(nil, acc), do: acc
  defp flush(pending, acc), do: [pending | acc]

  defp skip_to(source, needle) do
    case :binary.split(source, needle) do
      [_before, rest] -> rest
      [_only] -> ""
    end
  end

  defp skip_rune(<<?\\, _escaped, rest::binary>>), do: skip_to(rest, "'")
  defp skip_rune(source), do: skip_to(source, "'")

  defp interpreted(<<?", rest::binary>>, acc), do: {IO.iodata_to_binary(acc), rest}
  defp interpreted(<<>>, acc), do: {IO.iodata_to_binary(acc), ""}

  defp interpreted(<<?\\, escape, rest::binary>>, acc) do
    case unescape(escape, rest) do
      {value, rest} -> interpreted(rest, [acc, value])
    end
  end

  defp interpreted(<<char::utf8, rest::binary>>, acc),
    do: interpreted(rest, [acc, <<char::utf8>>])

  defp interpreted(<<byte, rest::binary>>, acc), do: interpreted(rest, [acc, <<byte>>])

  defp unescape(?n, rest), do: {"\n", rest}
  defp unescape(?t, rest), do: {"\t", rest}
  defp unescape(?r, rest), do: {"\r", rest}
  defp unescape(?a, rest), do: {"\a", rest}
  defp unescape(?b, rest), do: {"\b", rest}
  defp unescape(?f, rest), do: {"\f", rest}
  defp unescape(?v, rest), do: {"\v", rest}
  defp unescape(?\\, rest), do: {"\\", rest}
  defp unescape(?", rest), do: {"\"", rest}
  defp unescape(?', rest), do: {"'", rest}

  # Go's octal escape is exactly three digits -- `"\101"` is `"A"` and
  # `"\000"` is a NUL -- so it cannot be read a digit at a time. Anything else
  # starting with a digit is not a legal Go escape at all, and falls to the
  # clause below that keeps the character as written.
  defp unescape(digit, <<second, third, rest::binary>>)
       when digit in ?0..?7 and second in ?0..?7 and third in ?0..?7,
       do: {<<String.to_integer(<<digit, second, third>>, 8)>>, rest}

  defp unescape(?x, <<digits::binary-size(2), rest::binary>>),
    do: {<<String.to_integer(digits, 16)>>, rest}

  defp unescape(?u, <<digits::binary-size(4), rest::binary>>),
    do: {<<String.to_integer(digits, 16)::utf8>>, rest}

  defp unescape(?U, <<digits::binary-size(8), rest::binary>>),
    do: {<<String.to_integer(digits, 16)::utf8>>, rest}

  defp unescape(char, rest), do: {<<char>>, rest}

  # A raw literal is verbatim except that Go drops carriage returns.
  defp raw(<<?`, rest::binary>>, acc), do: {acc |> IO.iodata_to_binary() |> strip_cr(), rest}
  defp raw(<<>>, acc), do: {acc |> IO.iodata_to_binary() |> strip_cr(), ""}
  defp raw(<<char::utf8, rest::binary>>, acc), do: raw(rest, [acc, <<char::utf8>>])
  defp raw(<<byte, rest::binary>>, acc), do: raw(rest, [acc, <<byte>>])

  defp strip_cr(value), do: String.replace(value, "\r", "")
end
