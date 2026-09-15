#!/bin/sh
# Launch the extracted Burrito payload the way Burrito's Zig wrapper does
# (erlexec directly, same flags, same env), but with -mode under our
# control. Used to price the wrapper itself and the embedded/interactive
# difference.
#
#   bench/erlexec_direct.sh <interactive|embedded> <args...>
set -eu
P="$HOME/Library/Application Support/.burrito/coldstart_erts-16.1_0.1.0"
mode=$1; shift
ROOTDIR="$P" BINDIR="$P/erts-16.1/bin" RELEASE_ROOT="$P" RELEASE_SYS_CONFIG="$P/releases/0.1.0/sys" __BURRITO=1 \
  exec "$P/erts-16.1/bin/erlexec" -elixir ansi_enabled true -noshell -s elixir start_cli \
  -mode "$mode" -setcookie x -boot "$P/releases/0.1.0/start" -boot_var RELEASE_LIB "$P/lib" \
  -args_file "$P/releases/0.1.0/vm.args" -config "$P/releases/0.1.0/sys" -extra "$@"
