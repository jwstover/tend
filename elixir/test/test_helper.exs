# Two tags gate the Go parity corpus (see `Tend.Template.Parity`).
#
#   * `go` needs a Go toolchain on the PATH to render against. It is excluded
#     only when there is none, so a developer machine with Go runs the live
#     comparison by default and never has to remember a flag.
#   * `tend_db` renders a real user's tend.db, a machine-specific file that
#     cannot be checked in. Always excluded by default so `mix test` stays
#     hermetic; run it with
#
#         MIX_ENV=test mix test --include tend_db
#
# Whatever this run ends up covering, `Tend.Template.Parity.Banner` prints it
# after the suite -- a green run without Go is not a parity check, and it says
# so.
go = if Tend.Template.Parity.Go.available?(), do: [], else: [:go]

ExUnit.start(exclude: [:tend_db | go])
Tend.Template.Parity.Banner.install()
