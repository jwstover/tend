defmodule Mix.Tasks.Tend.Parity do
  @shortdoc "Renders every prompt through Go and Tend.Template and diffs the bytes"

  @moduledoc """
  Runs the Go parity harness. See `Tend.Template.Parity` for what it covers.

      MIX_ENV=test mix tend.parity
      MIX_ENV=test mix tend.parity --db ~/.local/share/tend/tend.db
      MIX_ENV=test mix tend.parity --update

  ## Options

    * `--db PATH` -- also render every `prompt_md` row in the `tend.db` at
      `PATH`. The file is copied first and the copy is opened read-only.
    * `--update` -- re-record `test/fixtures/parity/repo_prompts.jsonl` from
      Go before comparing. Do this when a Go test adds or changes a prompt
      template.

  The task lives in `test/support`, so it needs `MIX_ENV=test`; it exits
  non-zero when any case differs, and with no Go toolchain it exits zero after
  printing, at length, that it compared nothing.
  """

  use Mix.Task

  alias Tend.Template.Parity

  @impl true
  def run(argv) do
    {opts, _rest} = OptionParser.parse!(argv, strict: [db: :string, update: :boolean])

    case Parity.run(opts) do
      {:ok, report} ->
        Mix.shell().info(report)

      # Nothing to render against, which is not a pass. `Parity.run/1` hands
      # back the same banner the suite prints, so it goes to standard error
      # rather than sliding by in the normal output.
      {:skipped, report} ->
        Mix.shell().error(report)

      {:error, report} ->
        Mix.shell().error(report)
        exit({:shutdown, 1})
    end
  end
end
