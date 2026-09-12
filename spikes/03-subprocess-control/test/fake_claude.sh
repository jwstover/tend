#!/bin/sh
# Stand-in for `claude -p --output-format stream-json`: emits one JSON object
# per line on a schedule, spawns grandchildren the way claude does (an MCP
# server that lives for the whole run, a Bash tool's shell running a long
# command), and exits or runs forever.
#
#   fake_claude.sh finite   6 lines 300 ms apart, line 3 is ~40 KB, then exit 0
#   fake_claude.sh forever  a line every 300 ms until killed
#   fake_claude.sh readstdin  like forever, but first consumes one line of
#                             stdin (models a child that reads its stdin)
#
# Grandchildren are `sh -c` wrappers tagged with a marker in their argv (the
# two-command body stops sh from exec-optimizing the marker away), each
# running a `sleep` great-grandchild: the claude -> sh -> command shape of
# the Bash tool.
mode=${1:-finite}
marker=${2:-spike3}

# "MCP server": lives for the whole run.
sh -c "sleep 600; : $marker-mcp" &
# "Bash tool": a shell running a long command.
sh -c "sleep 600; : $marker-bash" &

if [ "$mode" = readstdin ]; then
  read -r _junk
fi

emit() {
  printf '{"type":"fake","n":%d,"pid":%d,"payload":"%s"}\n' "$1" "$$" "$2"
}

if [ "$mode" = finite ]; then
  big=$(head -c 40000 /dev/zero | tr '\0' 'x')
  i=1
  while [ "$i" -le 6 ]; do
    if [ "$i" -eq 3 ]; then emit "$i" "$big"; else emit "$i" "short"; fi
    sleep 0.3
    i=$((i + 1))
  done
  # A well-behaved child cleans up its own grandchildren on exit.
  for j in $(jobs -p); do pkill -P "$j" 2>/dev/null; kill "$j" 2>/dev/null; done
  exit 0
fi

i=1
while :; do
  emit "$i" "tick"
  sleep 0.3
  i=$((i + 1))
done
