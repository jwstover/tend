# tend

A fast, keyboard-driven task tracker for the terminal. Capture with `tend add`, then *tend* to your tasks in the TUI. It also runs and tracks Claude Code sessions and multi-step agent workflows tied to tasks.

Everything lives in one local SQLite file. There's no daemon and no account.

## Setup

### 1. Install

**From a release (recommended).** Download the archive for your platform (macOS or Linux, amd64 or arm64) from the [releases page](https://github.com/jwstover/tend/releases), extract it, and put `tend` on your `PATH`:

```sh
tar -xzf tend_<version>_<os>_<arch>.tar.gz
mv tend /usr/local/bin/
```

**With Go** (1.26.4+):

```sh
go install github.com/jwstover/tend/cmd/tend@latest
```

**From source:**

```sh
git clone https://github.com/jwstover/tend.git
cd tend
make install   # go install ./cmd/tend
```

Check that it worked:

```sh
tend version
```

### 2. Run it

```sh
tend
```

On first run, tend creates its database at `~/.local/share/tend/tend.db` (or `$XDG_DATA_HOME/tend/tend.db`). To use a different file, pass `--db <path>` or set `TEND_DB`.

That's all you need for task tracking. The rest of the setup is optional.

### 3. Optional extras

| For | Install | Notes |
| --- | --- | --- |
| Claude Code sessions and workflows | [`claude`](https://docs.claude.com/en/docs/claude-code) and `tmux` on your `PATH` | Without them, the session and workflow features turn themselves off. |
| Scheduled workflow runs | `tend schedule install` | Installs a launchd agent (macOS) or systemd timer (Linux) that runs `tend schedule tick` every minute. Remove it with `tend schedule uninstall`. |
| Clickable desktop notifications (macOS) | `brew install terminal-notifier` | Without it, notifications still appear but clicking them does nothing. See the note below. |
| Jira titles for pasted issue URLs | `tend auth jira login` | Your credentials are stored in the OS keychain. |

**terminal-notifier note:** after each `brew install` or `brew upgrade`, register it once, or macOS won't send its notifications:

```sh
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
  -f "$(brew --prefix)/Cellar/terminal-notifier/<version>/terminal-notifier.app"
```

## Usage

```sh
tend                      # open the TUI
tend add "call the bank"  # capture to the inbox; doesn't open the TUI
echo "idea" | tend a      # capture from stdin
tend ls                   # print the live task list
tend log "shipped X"      # add a standup note
tend standup              # print a standup summary as markdown
```

In the TUI, press `?` to see all the key bindings.

## Development

```sh
make build      # go build ./...
make test       # go test ./...
make lint       # golangci-lint
make generate   # regenerate sqlc code after editing internal/store/queries
```

Commits follow [Conventional Commits](https://www.conventionalcommits.org/), and releases are cut automatically by release-please. See [AGENTS.md](AGENTS.md) for the architecture and conventions.
