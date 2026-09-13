defmodule Tend.Task.ProjectTest do
  use ExUnit.Case, async: true

  alias Tend.Task.Project

  # A fixed home directory, so the ~ cases do not depend on the machine.
  @home "/Users/tester"
  defp home, do: fn -> @home end

  # A port of TestParseTags in internal/task/project_test.go, case for case.
  describe "parse_tags/1" do
    for {name, input, want} <- [
          {"empty is no tags", "", []},
          {"whitespace only", "   \t ", []},
          {"single", "work", ["work"]},
          {"space separated", "work home", ["work", "home"]},
          {"comma separated", "work,home", ["work", "home"]},
          {"comma and space mixed", "work, home,  errands", ["work", "home", "errands"]},
          {"leading hashes are display sugar", "#work #home", ["work", "home"]},
          {"surrounding whitespace trimmed", "  work  ,  home  ", ["work", "home"]},
          {"duplicates dropped", "work work", ["work"]},
          {"duplicates folded case-insensitively, first spelling wins", "Work work WORK",
           ["Work"]},
          {"a bare hash is not a tag", "# work", ["work"]},
          {"case preserved as typed", "Work", ["Work"]}
        ] do
      test name do
        assert Project.parse_tags(unquote(input)) == unquote(want)
      end
    end

    test "a newline separates too" do
      assert Project.parse_tags("work\nhome") == ["work", "home"]
    end

    # Go folds the dedup key with strings.ToLower, whose simple mapping lowers
    # U+0130 to "i"; String.downcase/1's full mapping lowers it to "i" +
    # U+0307. Both expectations below are what Go's ParseTags returns, run.
    test "folds a dotted capital I into a plain i, as strings.ToLower does" do
      assert Project.parse_tags("İ i") == ["İ"]
    end

    test "keeps two tags Go keeps apart, rather than folding one away" do
      assert Project.parse_tags("İİ i̇i̇") == ["İİ", "i̇i̇"]
    end
  end

  # The prompt seeds from format_tags/1 and submits through parse_tags/1, so
  # opening it and pressing enter unchanged must be a no-op. A port of
  # TestFormatTagsRoundTripsThroughParseTags.
  test "format_tags/1 round trips through parse_tags/1" do
    original = ["alpha", "beta", "gamma"]
    assert original |> Project.format_tags() |> Project.parse_tags() == original
  end

  # A port of TestNormalizeTag in internal/task/project_test.go, case for case.
  describe "normalize_tag/1" do
    test "keeps a plain tag and strips the display hash" do
      assert Project.normalize_tag("work") == {:ok, "work"}
      assert Project.normalize_tag("#work") == {:ok, "work"}
      assert Project.normalize_tag("  #work  ") == {:ok, "work"}
      assert Project.normalize_tag("# work") == {:ok, "work"}
    end

    test "rejects anything that normalizes to nothing" do
      assert Project.normalize_tag("") == :error
      assert Project.normalize_tag("   ") == :error
      assert Project.normalize_tag("#") == :error
      assert Project.normalize_tag("  #  ") == :error
    end
  end

  # A port of TestNormalizeProjectName in internal/task/project_test.go.
  describe "normalize_name/1" do
    test "trims surrounding whitespace" do
      assert Project.normalize_name("  tend  ") == {:ok, "tend"}
    end

    test "preserves case as typed" do
      assert Project.normalize_name("Work") == {:ok, "Work"}
    end

    test "rejects a blank name" do
      assert Project.normalize_name("   ") == {:error, :empty_project_name}
      assert Project.normalize_name("") == {:error, :empty_project_name}
    end
  end

  # A port of TestNormalizeProjectCwd in internal/task/project_cwd_test.go. Go
  # reads the real home directory and skips when there is none; the port takes
  # one, so the cases are fixed.
  describe "normalize_cwd/2" do
    test "blank is the stored form of no default" do
      assert Project.normalize_cwd("", home()) == ""
      assert Project.normalize_cwd("   ", home()) == ""
    end

    test "an absolute path is kept" do
      assert Project.normalize_cwd("/tmp/app", home()) == "/tmp/app"
    end

    test "surrounding whitespace and a trailing separator go" do
      assert Project.normalize_cwd("  /tmp/app/  ", home()) == "/tmp/app"
    end

    test "the path is cleaned lexically" do
      assert Project.normalize_cwd("/tmp//app/./src/..", home()) == "/tmp/app"
    end

    test "a leading ~ expands to the home directory" do
      assert Project.normalize_cwd("~", home()) == @home
      assert Project.normalize_cwd("~/code/app", home()) == @home <> "/code/app"
    end

    test "a ~ that isn't the home shorthand is left alone" do
      # "~other" is someone else's home, and expanding it wrong would be worse
      # than not expanding it.
      assert Project.normalize_cwd("~other/code", home()) == "~other/code"
    end

    test "a relative path stays relative" do
      assert Project.normalize_cwd("relative/dir", home()) == "relative/dir"
    end

    test "~ is left alone when there is no home directory to expand against" do
      none = fn -> nil end
      assert Project.normalize_cwd("~/code/app", none) == "~/code/app"
      assert Project.normalize_cwd("~", fn -> "" end) == "~"
    end

    test "a .. that would climb past the root is dropped" do
      assert Project.normalize_cwd("/../tmp", home()) == "/tmp"
      assert Project.normalize_cwd("../tmp", home()) == "../tmp"
    end

    test "it never touches the filesystem: a path that does not exist survives" do
      assert Project.normalize_cwd("/definitely/not/here", home()) == "/definitely/not/here"
    end

    test "it defaults to the real home directory" do
      # The production arity, exercised only for the shape of its answer: the
      # expansion is absolute whatever this machine's home happens to be.
      case System.user_home() do
        nil -> assert Project.normalize_cwd("~/x") == "~/x"
        "" -> assert Project.normalize_cwd("~/x") == "~/x"
        home -> assert Project.normalize_cwd("~/x") == Path.join(home, "x")
      end
    end
  end

  describe "the struct" do
    test "a bare project defaults to Go's zero values" do
      assert %Project{} == %Project{
               id: 0,
               name: "",
               sort_order: 0,
               archived_at: nil,
               created_at: nil,
               updated_at: nil,
               cwd: "",
               live_count: 0
             }
    end

    test "archived?/1 follows archived_at" do
      refute Project.archived?(%Project{})
      assert Project.archived?(%Project{archived_at: DateTime.utc_now()})
    end

    test "the default project is the seeded Unsorted row" do
      assert Project.default_id() == 1
    end
  end
end
