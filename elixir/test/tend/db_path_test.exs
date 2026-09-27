defmodule Tend.DBPathTest do
  use ExUnit.Case, async: true

  alias Tend.DBPath

  # The whole environment, so a test can say exactly what is and is not set.
  defp env(vars), do: fn name -> Map.get(vars, name) end
  defp home(path), do: fn -> path end

  describe "resolve/3 mirrors resolveDBPath in internal/cli/root.go" do
    test "the --db flag wins over everything" do
      vars = env(%{"TEND_DB" => "/env/tend.db", "XDG_DATA_HOME" => "/xdg"})

      assert DBPath.resolve("/flag/tend.db", vars, home("/home/j")) == "/flag/tend.db"
    end

    test "TEND_DB comes next" do
      vars = env(%{"TEND_DB" => "/env/tend.db", "XDG_DATA_HOME" => "/xdg"})

      assert DBPath.resolve(nil, vars, home("/home/j")) == "/env/tend.db"
    end

    test "then XDG_DATA_HOME/tend/tend.db" do
      vars = env(%{"XDG_DATA_HOME" => "/xdg"})

      assert DBPath.resolve(nil, vars, home("/home/j")) == "/xdg/tend/tend.db"
    end

    test "then ~/.local/share/tend/tend.db" do
      assert DBPath.resolve(nil, env(%{}), home("/home/j")) == "/home/j/.local/share/tend/tend.db"
    end

    test "and tend.db when there is no home directory to anchor to" do
      assert DBPath.resolve(nil, env(%{}), home(nil)) == "tend.db"
    end

    test "an empty flag or variable counts as unset, the way os.Getenv does" do
      vars = env(%{"TEND_DB" => "", "XDG_DATA_HOME" => ""})

      assert DBPath.resolve("", vars, home("/home/j")) == "/home/j/.local/share/tend/tend.db"
    end
  end

  test "resolve/1 reads the real environment" do
    assert DBPath.resolve("/flag/tend.db") == "/flag/tend.db"
    assert is_binary(DBPath.resolve(nil))
  end
end
