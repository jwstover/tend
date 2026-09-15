#!/bin/sh
# Runs every scenario. `fake` needs no network; the claude scenarios call
# the real CLI on haiku (a few cents). Pass `fake` to run only the offline
# part.
#
#   test/drive.sh            everything
#   test/drive.sh fake       offline scenarios + BEAM crash test
set -u
cd "$(dirname "$0")/.."
mkdir -p tmp
fail=0
mode=${1:-all}   # read before the `set --` below reuses the positional parameters

run() {
  echo
  echo "#### mix run -- $*"
  mix run -e 'Subproc.main(System.argv())' -- "$@" || fail=1
}

# ---------------------------------------------------------------- BEAM crash
# What MuonTrap uniquely buys: the BEAM dies (SIGKILL, no chance to run
# terminate/2) and muontrap, seeing EOF on its stdin, takes the child down.
echo
echo "#### beam-crash: kill -9 the BEAM while the fake child runs"
pidfile=tmp/hold.pids
rm -f "$pidfile"
mix run -e 'Subproc.main(System.argv())' -- hold "$pidfile" &
mixpid=$!
i=0
while [ ! -s "$pidfile" ] && [ $i -lt 100 ]; do sleep 0.2; i=$((i + 1)); done
if [ ! -s "$pidfile" ]; then
  echo "FAIL  hold scenario never wrote its pids"; kill $mixpid; fail=1
else
  set -- $(cat "$pidfile")
  mt=$1; child=$2; shift 2; grand="$*"
  # `mix run` may itself be a wrapper; the BEAM is muontrap's parent.
  beam=$(ps -o ppid= -p "$mt" | tr -d ' ')
  echo "beam=$beam muontrap=$mt child=$child grandchildren=$grand"
  kill -9 "$beam"
  sleep 2.5   # delay_to_sigkill is 1000 ms in hold/1
  if kill -0 "$mt" 2>/dev/null; then echo "FAIL  muontrap $mt survived the BEAM's death"; fail=1
  else echo "PASS  muontrap $mt died with the BEAM"; fi
  if kill -0 "$child" 2>/dev/null; then echo "FAIL  child $child survived the BEAM's death"; fail=1
  else echo "PASS  child $child was killed by muontrap after the BEAM died"; fi
  left=0
  for g in $grand; do if kill -0 "$g" 2>/dev/null; then left=$((left + 1)); kill -9 "$g"; fi; done
  echo "NOTE  grandchildren orphaned after BEAM death (MuonTrap alone, no cgroup): $left of $#"
  wait $mixpid 2>/dev/null
fi

# ------------------------------------------------------------------ scenarios
run fake
if [ "$mode" = all ]; then
  run claude-run
  run claude-cancel
  run claude-cancel-port-close
fi

echo
if [ $fail -eq 0 ]; then echo "DRIVE: ALL PASS"; else echo "DRIVE: FAILURES"; fi
exit $fail
