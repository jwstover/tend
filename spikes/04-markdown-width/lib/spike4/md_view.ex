defmodule Spike4.MdView do
  @moduledoc """
  The smallest view that shows a task body the way the detail pane would:
  Breeze's `<.markdown>` block, full height, at a given width.
  """

  use Breeze.View
  import Breeze.Blocks, only: [markdown: 1]

  @impl true
  def mount(opts, term) do
    {:ok, assign(term, body: Keyword.fetch!(opts, :body), width: Keyword.fetch!(opts, :width))}
  end

  @impl true
  def render(assigns) do
    ~H"""
    <box class="width-full height-full">
      <.markdown id="body" content={@body} width={@width} />
    </box>
    """
  end
end
