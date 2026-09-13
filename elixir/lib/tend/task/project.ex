defmodule Tend.Task.Project do
  @moduledoc """
  A project: the group every task belongs to exactly one of.

  A port of `internal/task/project.go`. The TUI's leftmost column lists
  projects and scopes the task list to the selected one. Deliberately flat --
  a project has no parent (see `docs/projects-plan.md` §7).

  Tag normalization (`normalize_tag/1`, `parse_tags/1`, `format_tags/1`) lives
  here because it lives in `project.go`; tags are not project-scoped, and the
  port keeps the Go layout rather than improving on it, so the two trees stay
  diffable.
  """

  @default_id 1

  @typedoc "A project, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          name: String.t(),
          sort_order: integer(),
          archived_at: DateTime.t() | nil,
          created_at: DateTime.t() | nil,
          updated_at: DateTime.t() | nil,
          cwd: String.t(),
          live_count: integer()
        }

  defstruct id: 0,
            name: "",
            sort_order: 0,
            archived_at: nil,
            created_at: nil,
            updated_at: nil,
            # Where a new Claude session on one of this project's tasks starts
            # unless the task already has a session to copy from. A project is
            # typically scoped to one application checkout, so this is almost
            # always the right answer and saves retyping it per task. "" means
            # unset; it is a prompt prefill, never a constraint on where a
            # session may run.
            cwd: "",
            # Live top-level tasks in this project: the population the list
            # view renders as rows, so the number shown beside a project
            # matches what selecting it produces. Zero unless the project came
            # from ListProjects.
            live_count: 0

  @doc """
  The id of the seeded "Unsorted" project (migration 00007).

  It is the fallback capture target when no active project is set, and the row
  every deleted project's tasks are reassigned to, so it must always exist --
  deleting it fails with `:protected_project`.
  """
  @spec default_id() :: integer()
  def default_id, do: @default_id

  @doc """
  Whether the project is hidden from the projects column.
  """
  @spec archived?(t()) :: boolean()
  def archived?(%__MODULE__{archived_at: archived_at}), do: archived_at != nil

  @doc """
  Trims surrounding whitespace and rejects blank names.

  Case is preserved as typed; the schema's `NOCASE` collation is what makes
  "Work" and "work" the same project.
  """
  @spec normalize_name(String.t()) :: {:ok, String.t()} | {:error, :empty_project_name}
  def normalize_name(s) when is_binary(s) do
    case String.trim(s) do
      "" -> {:error, :empty_project_name}
      name -> {:ok, name}
    end
  end

  @doc """
  Canonicalizes a default working directory as typed at a prompt.

  Surrounding whitespace goes, a leading `~` expands to the home directory (a
  terminal user types paths that way, and the process that runs the session
  does not expand it), and the result is cleaned. Blank normalizes to `""`,
  which is the stored form of "no default". The path is deliberately not
  checked for existence: it is a prefill the user can still edit, and a
  checkout that isn't cloned yet is a fine thing to plan for.

  A `~` that is not the home shorthand is left alone: `~other` is someone
  else's home, and expanding it wrong would be worse than not expanding it.

  `home` exists so the tests can drive the expansion without depending on the
  machine they run on; production calls pass nothing, the same arrangement
  `Tend.DBPath.resolve/3` uses.
  """
  @spec normalize_cwd(String.t(), (-> String.t() | nil)) :: String.t()
  def normalize_cwd(s, home \\ &System.user_home/0) when is_binary(s) do
    case String.trim(s) do
      "" -> ""
      path -> path |> expand_tilde(home) |> clean()
    end
  end

  defp expand_tilde("~" = path, home), do: with_home(path, home, "")
  defp expand_tilde("~/" <> rest, home), do: with_home("~/" <> rest, home, "/" <> rest)
  defp expand_tilde(path, _home), do: path

  defp with_home(original, home, suffix) do
    case home.() do
      nil -> original
      "" -> original
      dir -> dir <> suffix
    end
  end

  # A lexical port of Go's filepath.Clean: collapse repeated separators, drop
  # "." elements, resolve each inner ".." against the element before it, and
  # discard a ".." that would climb past the root. Purely textual -- it never
  # touches the filesystem, and it must not, since the path may not exist yet.
  defp clean(path) do
    rooted? = String.starts_with?(path, "/")

    cleaned =
      path
      |> String.split("/")
      |> Enum.reduce([], &clean_segment(&1, &2, rooted?))
      |> Enum.reverse()
      |> Enum.join("/")

    cond do
      rooted? -> "/" <> cleaned
      cleaned == "" -> "."
      true -> cleaned
    end
  end

  defp clean_segment("", acc, _rooted?), do: acc
  defp clean_segment(".", acc, _rooted?), do: acc
  defp clean_segment("..", [".." | _] = acc, _rooted?), do: [".." | acc]
  defp clean_segment("..", [], rooted?), do: if(rooted?, do: [], else: [".."])
  defp clean_segment("..", [_parent | rest], _rooted?), do: rest
  defp clean_segment(segment, acc, _rooted?), do: [segment | acc]

  @doc """
  Canonicalizes one tag: surrounding whitespace and a leading `#` are dropped
  (the `#` is display sugar in the list row, not part of the stored name).

  Returns `:error` for anything that normalizes to nothing.
  """
  @spec normalize_tag(String.t()) :: {:ok, String.t()} | :error
  def normalize_tag(s) when is_binary(s) do
    tag =
      s
      |> String.trim()
      |> strip_leading_hash()
      |> String.trim()

    if tag == "", do: :error, else: {:ok, tag}
  end

  defp strip_leading_hash("#" <> rest), do: rest
  defp strip_leading_hash(s), do: s

  @doc """
  Splits a free-text tag prompt into normalized tag names.

  Commas and whitespace both separate, so `"work, home"` and `"work home"` are
  the same input. Duplicates are dropped case-insensitively, matching the
  schema's `NOCASE` uniqueness, and the first spelling of a tag wins. Returns
  a list, always -- a cleared prompt is an explicit "no tags" rather than an
  absent value.
  """
  @spec parse_tags(String.t()) :: [String.t()]
  def parse_tags(s) when is_binary(s) do
    s
    |> String.split([",", " ", "\t", "\n"], trim: true)
    |> Enum.flat_map(fn field ->
      case normalize_tag(field) do
        {:ok, tag} -> [tag]
        :error -> []
      end
    end)
    |> Enum.uniq_by(&String.downcase/1)
  end

  @doc """
  Renders tags back into the space-separated form `parse_tags/1` accepts, for
  seeding the tag prompt with a task's current tags.
  """
  @spec format_tags([String.t()]) :: String.t()
  def format_tags(tags) when is_list(tags), do: Enum.join(tags, " ")
end
