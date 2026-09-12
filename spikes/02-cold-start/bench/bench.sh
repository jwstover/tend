#!/bin/sh
# Spike 2 benchmark: Go tend vs the Burrito+exqlite one-shot binary.
#
#   bench/bench.sh [N]
#
# Env: GO_BIN (default tmp/tend-go, built by `go build -o tmp/tend-go
# ../../cmd/tend`), EX_BIN (default burrito_out/coldstart_macos_arm64),
# EX_LABEL (name for the Elixir column). Needs sqlite3 and perl.
#
# The database is seeded by the Go binary (so it carries the real schema
# and goose version table) and copied fresh for each scenario, so
# neither side runs against a database the other has grown.
set -eu
cd "$(dirname "$0")/.."

N=${1:-20}
GO_BIN=${GO_BIN:-tmp/tend-go}
EX_BIN=${EX_BIN:-burrito_out/coldstart_macos_arm64}
EX_LABEL=${EX_LABEL:-burrito}
T=bench/time.pl
W=tmp/bench
SEED=$W/seed.db

[ -x "$GO_BIN" ] || { echo "no Go binary at $GO_BIN (go build -o tmp/tend-go ../../cmd/tend)"; exit 1; }
[ -x "$EX_BIN" ] || { echo "no Elixir binary at $EX_BIN (MIX_ENV=prod mix release)"; exit 1; }

rm -rf "$W"; mkdir -p "$W"
"$GO_BIN" add --db "$SEED" "seed task" >/dev/null
sqlite3 "$SEED" "INSERT INTO agent_sessions (task_id, external_id, cwd, label) VALUES (1, 'sess-1', '/tmp', 'seed');"

fresh() { rm -f "$W/$1.db" "$W/$1.db-wal" "$W/$1.db-shm"; cp "$SEED" "$W/$1.db"; echo "$W/$1.db"; }

# Correctness before speed: each side does what it claims.
"$EX_BIN" add --db "$(fresh check)" "hello from elixir" | grep -q 'added #2 to Unsorted: hello from elixir'
printf '{"session_id":"sess-1","hook_event_name":"Stop","cwd":"/tmp"}' | "$EX_BIN" agent-hook Stop --db "$W/check.db"
[ "$(sqlite3 "$W/check.db" "SELECT status FROM agent_sessions WHERE external_id='sess-1'")" = idle ]
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"bench","version":"0"}}}' \
  | "$EX_BIN" mcp --task-id 1 --db "$W/check.db" | grep -q '"serverInfo"'
echo "sanity: ok"
echo

HOOK='{"session_id":"sess-1","hook_event_name":"Stop","cwd":"/tmp"}'
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"bench","version":"0"}}}
'

echo "N=$N runs each; ms"
echo "--- floor (no database) ---"
$T -n "$N" --label "go      version"              -- "$GO_BIN" version
$T -n "$N" --label "$EX_LABEL noop"               -- "$EX_BIN" noop
echo "--- tend add ---"
$T -n "$N" --label "go      add"                  -- "$GO_BIN" add --db "$(fresh go-add)" "bench task"
$T -n "$N" --label "$EX_LABEL add"                -- "$EX_BIN" add --db "$(fresh ex-add)" "bench task"
echo "--- tend agent-hook Stop ---"
$T -n "$N" --stdin "$HOOK" --label "go      agent-hook"     -- "$GO_BIN" agent-hook Stop --db "$(fresh go-hook)"
$T -n "$N" --stdin "$HOOK" --label "$EX_LABEL agent-hook"   -- "$EX_BIN" agent-hook Stop --db "$(fresh ex-hook)"
echo "--- tend mcp: spawn to initialize response (1st-ln), then EOF to exit (total) ---"
$T -n "$N" --stdin "$INIT" --first-line --label "go      mcp"   -- "$GO_BIN" mcp --task-id 1 --db "$(fresh go-mcp)"
$T -n "$N" --stdin "$INIT" --first-line --label "$EX_LABEL mcp" -- "$EX_BIN" mcp --task-id 1 --db "$(fresh ex-mcp)"
