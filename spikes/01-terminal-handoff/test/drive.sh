#!/usr/bin/env bash
# Drives the spike through every handoff scenario inside a scratch tmux
# server and asserts on captured screens. No real terminal needed.
#
#   test/drive.sh            # all scenarios (needs claude, nvim, tmux)
#   SKIP_CLAUDE=1 test/drive.sh
#
# tmux servers used: spike-test (the app), spike-host (an outer client we
# attach and detach), spike-handoff (the app's own claude session socket).
set -u

cd "$(dirname "$0")/.." || exit 1
ROOT=$PWD
S=(tmux -L spike-test)
H=(tmux -L spike-host)
TRACE=$ROOT/tmp/handoff.log
FAILS=0

cleanup() {
  "${S[@]}" kill-server 2>/dev/null
  "${H[@]}" kill-server 2>/dev/null
  tmux -L spike-handoff kill-server 2>/dev/null
}
trap cleanup EXIT

cap() { "${S[@]}" capture-pane -t app -p; }
keys() { "${S[@]}" send-keys -t app "$@"; }

# expect <label> <seconds> <grep -E pattern>: poll the pane until it matches.
expect() {
  local label=$1 secs=$2 pat=$3 i
  for ((i = 0; i < secs * 4; i++)); do
    if cap | grep -Eq -- "$pat"; then
      echo "PASS  $label"
      return 0
    fi
    sleep 0.25
  done
  echo "FAIL  $label (no /$pat/ within ${secs}s)"
  echo "----- pane -----"; cap | grep -v '^$'; echo "----------------"
  FAILS=$((FAILS + 1))
  return 1
}

# expect_trace <label> <seconds> <pattern>: same, against the handoff trace.
expect_trace() {
  local label=$1 secs=$2 pat=$3 i
  for ((i = 0; i < secs * 4; i++)); do
    if grep -Eq -- "$pat" "$TRACE" 2>/dev/null; then
      echo "PASS  $label"
      return 0
    fi
    sleep 0.25
  done
  echo "FAIL  $label (no /$pat/ in trace within ${secs}s)"
  FAILS=$((FAILS + 1))
  return 1
}

cleanup
rm -f "$TRACE"
mix compile >/dev/null 2>&1 || { echo "FAIL  mix compile"; exit 1; }

echo "== start"
"${S[@]}" new-session -d -s app -x 110 -y 30 -c "$ROOT"
sleep 1
keys "clear; bin/spike; echo APP_EXIT=\$?" Enter
expect "app renders" 30 'state idle'
expect "app size 110x30" 5 'size 110x30'

echo "== probe: child owns tty, input reaches it, WINCH forwarded, full redraw"
keys p
expect "probe on main screen" 5 'probe: type lines'
expect "probe has no controlling tty (setsid)" 2 'probe: pid pgid tpgid tty -> +[0-9]+ +[0-9]+ +0 +\?\?'
expect "probe stdin is blocking" 2 'probe: stdin nonblock=0'
keys "hello" Enter
expect "typed line reached child" 5 'probe: got \[hello\]'
"${S[@]}" resize-window -t app -x 100 -y 28
expect "WINCH reached child" 5 'probe: WINCH -> 28 100'
keys exit Enter
expect "app back after probe" 5 'probe: exit=0 .* winch=1'
expect "app repainted at new size" 5 'size 100x28'
expect "header repainted (full redraw)" 2 '^Spike 1: terminal handoff'

echo "== editor: $EDITOR round trips"
for i in 1 2; do
  keys e
  expect "nvim $i on tty" 10 'Edit me, then quit the editor'
  keys Escape ":q" Enter
  expect "app back after nvim $i" 10 "editor\\(.*\\): exit=0"
done
expect "clock still ticking" 3 "clock $(date -u +%H:%M)"

if [ -z "${SKIP_CLAUDE:-}" ] && command -v claude >/dev/null; then
  echo "== claude in tmux: launch, outer detach/reattach at new size, inner detach, reattach, exit"
  keys t
  expect "claude inside tmux on tty" 40 'Claude Code v'
  # Outer tmux client comes and goes at different sizes mid-child.
  "${H[@]}" new-session -d -s h1 -x 120 -y 40 "tmux -L spike-test attach -t app"
  sleep 2
  "${S[@]}" detach-client -s app
  sleep 1
  "${H[@]}" new-session -d -s h2 -x 100 -y 35 "tmux -L spike-test attach -t app"
  expect_trace "WINCH forwarded to tmux client" 5 'WINCH -> kill -WINCH'
  expect "claude relaid out at 100 cols" 5 '^─{100}$'
  # Inner detach: what the user's prefix-d does.
  tmux -L spike-handoff detach-client -s spike
  expect "app back after detach" 10 "tmux\\(claude\\): exit=0"
  if tmux -L spike-handoff has-session -t spike 2>/dev/null; then
    echo "PASS  claude session survives detach"
  else
    echo "FAIL  claude session survives detach"; FAILS=$((FAILS + 1))
  fi
  keys t
  expect "reattached to same session" 20 'Claude Code v'
  keys "/exit"; sleep 1; keys Enter
  expect "app back after /exit" 15 "handoffs 5"
  if tmux -L spike-handoff has-session -t spike 2>/dev/null; then
    echo "FAIL  claude session gone after /exit"; FAILS=$((FAILS + 1))
  else
    echo "PASS  claude session gone after /exit"
  fi
  "${H[@]}" kill-server 2>/dev/null
else
  echo "== claude scenarios skipped"
fi

echo "== probe again: stdin is blocking again for a plain shell"
keys p
expect "probe waits for input" 5 'probe: type lines'
expect "probe stdin is blocking after tmux/claude" 2 'probe: stdin nonblock=0'
sleep 2
# The main screen still shows the first probe's output, so look for the
# app frame instead: if the probe had exited, Breeze would be back up.
if cap | grep -q 'state idle'; then
  echo "FAIL  probe exited on its own"; FAILS=$((FAILS + 1))
  echo "----- pane -----"; cap | grep -v '^$'; echo "----------------"
else
  echo "PASS  probe blocked on read"
fi
keys exit Enter
expect "app back after second probe" 5 'state idle'

echo "== quit"
keys q
expect "clean exit, terminal restored" 10 'APP_EXIT=0'

echo
if [ "$FAILS" -eq 0 ]; then echo "ALL PASS"; else echo "$FAILS FAILED"; fi
exit "$FAILS"
