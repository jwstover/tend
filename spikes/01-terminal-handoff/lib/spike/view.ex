defmodule Spike.View do
  @moduledoc """
  One screen: a ticking clock (proves the event loop is alive again after
  a handoff), a state line, and a log of handoffs.

  Keys:

    * `e` — `$VISUAL`/`$EDITOR` on a temp file (tend's edit-body path)
    * `c` — `claude` directly
    * `t` — `claude` inside a tmux session on its own socket (tend's
      launch path; detach with the tmux prefix to come back)
    * `p` — a shell probe that prints tty / process-group facts, echoes
      lines, reports WINCH, exits on `exit`
    * `q` — quit
  """

  use Breeze.View

  alias Spike.Handoff

  @tick_ms 1_000
  @log_max 12

  @impl true
  def mount(_opts, term) do
    Process.send_after(self(), :tick, @tick_ms)

    {:ok,
     assign(term,
       clock: clock(),
       state: "idle",
       count: 0,
       log: [],
       otp: System.otp_release(),
       size: size(term)
     )}
  end

  @impl true
  def render(assigns) do
    ~H"""
    <box class="width-full height-full">
      <box class="bold">Spike 1: terminal handoff (Breeze 0.5.1, Termite 0.4.4, OTP {@otp})</box>
      <box>clock {@clock}   size {@size}   handoffs {@count}   state {@state}</box>
      <box class="text-muted">e $EDITOR   c claude   t claude in tmux   s shell in tmux   p probe   q quit</box>
      <box></box>
      <box :for={line <- @log}>{line}</box>
    </box>
    """
  end

  # -- keys ----------------------------------------------------------------

  @impl true
  def handle_event(:input, %{"key" => "q"}, term), do: {:stop, term}

  def handle_event(:input, %{"key" => key}, term) when key in ~w(e c t s p) do
    if Process.get(:handoff) do
      {:noreply, term}
    else
      case command(key) do
        {:error, msg} -> {:noreply, log(term, "#{key}: #{msg}")}
        {label, exe, args, opts} -> start(term, label, exe, args, opts)
      end
    end
  end

  def handle_event(_, _, term), do: {:noreply, term}

  # -- messages ------------------------------------------------------------

  @impl true
  def handle_info(:tick, term) do
    Process.send_after(self(), :tick, @tick_ms)

    # While a child owns the tty the term must not change: a changed term
    # makes Breeze render, and a render now would paint over the child.
    if Process.get(:handoff),
      do: {:noreply, term},
      else: {:noreply, assign(term, clock: clock())}
  end

  def handle_info({:handoff_done, label, result}, term) do
    Process.delete(:handoff)

    line =
      "#{clock()} #{label}: exit=#{inspect(result.status)} pid=#{result.os_pid} " <>
        "winch=#{result.winch} #{result.ms}ms"

    {:noreply,
     term
     |> assign(state: "idle", count: term.assigns.count + 1, clock: clock(), size: size(term))
     |> log(line)}
  end

  def handle_info(:resize, term), do: {:noreply, assign(term, size: size(term))}
  def handle_info(_msg, term), do: {:noreply, term}

  # -- handoff -------------------------------------------------------------

  # Returns the term unchanged on purpose (state lives in the process
  # dictionary until the child exits): Breeze only renders after an event
  # when the term changed, and the release sequence in Handoff must not
  # race a frame write.
  defp start(term, label, exe, args, opts) do
    view = self()
    ctx = Handoff.from_term(term)

    pid =
      spawn(fn ->
        result = Handoff.exec(ctx, exe, args, opts)
        send(view, {:handoff_done, label, result})
      end)

    Process.put(:handoff, pid)
    {:noreply, term}
  end

  defp command("e") do
    editor = System.get_env("VISUAL") || System.get_env("EDITOR") || "vi"
    [cmd | args] = String.split(editor)

    with {:ok, exe} <- find(cmd) do
      path = Path.join(System.tmp_dir!(), "spike-handoff-#{System.os_time(:second)}.md")
      File.write!(path, "# spike\n\nEdit me, then quit the editor.\n")
      {"editor(#{cmd})", exe, args ++ [path], []}
    end
  end

  defp command("c") do
    with {:ok, exe} <- find("claude") do
      {"claude", exe, [], cd: File.cwd!()}
    end
  end

  defp command("t") do
    with {:ok, tmux} <- find("tmux"), {:ok, claude} <- find("claude") do
      # Same shape as tend's agent.WrapTmux: a named session on a
      # dedicated socket, attached. -A attaches if it already exists so a
      # detached session is resumed rather than duplicated. TMUX is
      # removed so tmux allows the nested client when the spike itself
      # runs inside tmux.
      args = ["-L", "spike-handoff", "new-session", "-A", "-s", "spike", "--", claude]
      {"tmux(claude)", tmux, args, cd: File.cwd!(), env: [{"TMUX", false}]}
    end
  end

  defp command("p") do
    {"probe", "/bin/sh", ["-c", probe_script()], []}
  end

  # Same tmux shape as `t`, but running a shell: the tmux client's tty
  # behaviour without needing claude installed.
  defp command("s") do
    with {:ok, tmux} <- find("tmux") do
      shell = System.get_env("SHELL") || "/bin/sh"
      args = ["-L", "spike-handoff", "new-session", "-A", "-s", "spike-shell", "--", shell]
      {"tmux(shell)", tmux, args, cd: File.cwd!(), env: [{"TMUX", false}]}
    end
  end

  defp probe_script do
    ~S"""
    echo "probe: pid=$$ tty=$(tty)"
    echo "probe: pid pgid tpgid tty -> $(ps -o pid=,pgid=,tpgid=,tty= -p $$)"
    echo "probe: stty size -> $(stty size)"
    echo "probe: $(stty -a | tr '\n' ' ' | cut -c1-200)"
    echo "probe: stdin nonblock=$(perl -e 'use Fcntl; print((fcntl(STDIN, F_GETFL, 0) + 0) & O_NONBLOCK ? 1 : 0)')"
    trap 'echo "probe: WINCH -> $(stty size)"' WINCH
    echo "probe: type lines; exit to return"
    while :; do
      if IFS= read -r line; then
        [ "$line" = exit ] && { echo "probe: got exit line"; break; }
        echo "probe: got [$line]"
      else
        rc=$?
        [ $rc -gt 128 ] && continue
        echo "probe: read failed rc=$rc"
        break
      fi
    done
    echo "probe: bye"
    """
  end

  defp find(cmd) do
    case System.find_executable(cmd) do
      nil -> {:error, "#{cmd} not found on PATH"}
      exe -> {:ok, exe}
    end
  end

  # -- helpers -------------------------------------------------------------

  defp log(term, line) do
    assign(term, log: Enum.take([line | term.assigns.log], @log_max))
  end

  defp clock, do: Calendar.strftime(DateTime.utc_now(), "%H:%M:%S")

  defp size(%{terminal: %{size: %{width: w, height: h}}}), do: "#{w}x#{h}"
  defp size(_), do: "?"
end
