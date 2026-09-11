# Spike 4: markdown fidelity and ANSI width (Breeze 0.5.1 / BackBreeze 0.4.4)

**Verdict: FAIL on markdown, PASS with one small patch on width.**

`Breeze.Markdown` is a 189-line line-oriented renderer, not a CommonMark
implementation: rendering the parent task #206 body through it turns the
stack table into a run-on paragraph, merges the numbered Phase 0 list into
one block, and gives every heading the same black-on-yellow bar with the
`##`/`###` marks left in the text. That fails the task's "headings, tables,
code blocks, lists, links" bar on three of the five. The width primitives
are a different story: BackBreeze measures and truncates by grapheme
cluster, and every fixture the Go row renderers care about (styled text,
tend's glyph table, CJK, combining marks, ZWJ sequences, skin tones)
matches `x/ansi` exactly. The misses are one cell on flags, VS16 emoji
(`❤️`, `⚠️`) and keycaps, plus tabs and OSC 8 hyperlinks, and a 15-line
grapheme rule (`lib/spike4/gwidth.ex`) brings all 28 fixtures to parity.

Throwaway code, per task #207. Pinned to `breeze 0.5.1` (`back_breeze
0.4.4`); `jason` reads the Go reference, `earmark_parser` is only there to
price the fail path. Machine: Apple M4 Pro, macOS 26.6, OTP 28.1, Elixir
1.19.1, Go 1.26.4, `charm.land/glamour/v2 v2.0.0`,
`github.com/charmbracelet/x/ansi v0.11.7`.

## Run it

```sh
cd spikes/04-markdown-width
mix deps.get
test/drive.sh          # gocheck reference, then markdown / ast / width / gwidth
test/drive.sh 60       # same at 60 columns
```

`gocheck/` is its own Go module (so the root `go build ./...` and lint skip
it): `gocheck widths` dumps `x/ansi` `StringWidth`/`Truncate`/`Wrap`/`Strip`
over 28 fixture strings as JSON; `gocheck glamour FILE W` renders a file the
way `internal/tui/detail.go` does (`DarkStyle`, `WithWordWrap`). The Elixir
side reads those and prints PASS/FAIL per check. Rendered outputs land in
`tmp/` (`glamour-80.ans`, `breeze-direct-80.ans`, `breeze-frame-80.ans`) for
eyeballing with `cat`.

## What is in here

- `fixtures/body-206.md`: the parent task's body, verbatim. 66 non-blank
  lines: h2/h3 headings, a 3-column 8-row GFM table, bullet lists, two
  ordered lists, inline code, bold, one bare URL.
- `lib/spike4/markdown.ex`: renders the body through `Breeze.Markdown.render/2`
  directly and through the `<.markdown>` block under `Breeze.Test` (both
  agree line for line), then checks 16 constructs against the glamour
  render and prints what each renderer did.
- `lib/spike4/width.ex`: `BackBreeze.Utils.string_length/1`,
  `BackBreeze.String.truncate/2`, `BackBreeze.String.reflow/3` and the span
  path `BackBreeze.TextLayout.prepare/4` against the Go JSON, 41 checks per
  fixture.
- `lib/spike4/gwidth.ex`: the fail-path patch for width, as a standalone
  rule, run against the same reference.

## Markdown: pass criteria and what was observed

Body rendered at 80 columns. glamour: 207 lines; Breeze: 162.

| Construct | glamour | Breeze.Markdown | Result |
|---|---|---|---|
| headings | `## `/`### ` kept, styled per level | every heading line is one black-on-yellow bar padded to full width; the `##`/`###` marks stay in the text; h2 and h3 are indistinguishable | fail |
| table | 57 lines of `│`-ruled cells, text wrapped inside cells | zero table lines: the 10 source lines are joined into one paragraph and reflowed, `| Concern | Pick | Notes | |---|---|---| | TUI | ...` | fail |
| ordered list | 10 numbered lines, one item per paragraph | items are paragraph text: `1.` starts a paragraph, then `2.`, `3.`, `4.` run on mid-line (`...capture path. 3. Subprocess control.`) | fail |
| bullet list | 28 `•` items | 28 `•` items, continuation lines indented under the bullet | pass |
| fenced code | indented, colored | indented 4, colored, verbatim | pass |
| inline code | padded, colored | colored span | pass |
| bold | bold | bold span | pass |
| italic | italic | `*em*` and `_this_` left as literal text | fail |
| links | text, OSC 8 hyperlink | `text (url)`, no OSC 8 | pass (acceptable) |
| bare URL | underlined, OSC 8 | plain text | pass |
| nested list (2-space indent) | nested bullet | `  - nested` is neither a bullet nor indented: it becomes its own paragraph reading `- nested` | fail |
| blockquote | rule + indent | `> quoted line` literal | fail |
| horizontal rule | rule | `---` literal | fail |
| hard break (two trailing spaces) | line break | joined: `line one line two` | fail |
| width | wraps at 80 | wraps at 80, no line over | pass |
| block vs renderer | n/a | `<.markdown>` frame equals the direct render | pass |

Pass criterion was "headings, tables, code blocks, lists, and links render
acceptably compared to glamour". Code blocks, bullet lists and links pass;
headings, tables and ordered lists do not. Task bodies in this database are
written by Claude sessions and routinely contain all three (the fixture is
a typical one), so this is not an edge case.

### Fail path: cost of a fix

The renderer is `@moduledoc false`, regex-driven, and has no parser to
extend; the fix is a replacement, not a patch.

- **Vendored renderer (recommended): 2 to 3 days.** `earmark_parser`
  (pure Elixir, Apache-2.0, already a dependency of ExDoc so it is on every
  Elixir developer's machine) parses the body into an AST that has every
  construct Breeze drops: `mix run -- ast` shows `h2`, `h3`, `table`
  (thead/tbody), `ol` with 4 and 6 and 2 items, `ul`, `p`, `a`, `code`,
  `strong`, `em`. A `Tend.Markdown` walking that AST into `TextSpan`s with a
  glamour-shaped style table is the work: paragraphs and headings by level
  (half a day), nested `ul`/`ol` with continuation indents (half a day),
  tables with column sizing and in-cell wrapping the way glamour does it
  (one day, the only fiddly part), code blocks, blockquotes, rules, hard
  breaks and links (half a day), snapshot tests against `body-206.md` and
  a few smaller bodies (half a day). Output is spans, so it drops straight
  into Breeze's `<.scroll>` the way `Breeze.Markdown.render/2` does; the
  block itself is 15 lines and trivially reimplemented. No NIF: `mdex`
  (comrak) would also work but adds a Rust NIF to the Burrito bundle for no
  gain over earmark_parser here.
- **Upstream PR to Breeze: not a blocker, maybe a follow-up.** Replacing
  `Breeze.Markdown` upstream means adding a parser dependency to Breeze,
  which is the maintainer's call; the vendored module does not depend on
  it landing. Worth offering once it exists.

## Width: pass criteria and what was observed

Reference: `x/ansi` (`StringWidth`, `Truncate`, `Wrap`, `Strip`) over 28
fixtures: ASCII, SGR-styled text (16-color, 256-color, truecolor), the
state/session/chrome glyph tables from `internal/tui/styles.go`, a styled
list row, CJK, Hangul, basic emoji, NFD combining marks, stacked combining
marks, a ZWJ family, a flag, VS16 heart, text-presentation heart, keycap,
skin tone, Devanagari, Thai, zero-width space, soft hyphen, tab, an OSC 8
hyperlink, a long word, hyphenated words, a mixed paragraph.

| Check | Elixir primitive | Result |
|---|---|---|
| width of a styled string | `Utils.string_length/1` (per codepoint, SGR skipped) | 21/28. Misses: ZWJ family 13 vs 9, skin tone 7 vs 5, Devanagari 8 vs 7 (over-count: each codepoint of a cluster is summed); VS16 heart 6 vs 7, keycap 5 vs 6 (under-count); tab 3 vs 2; OSC 8 7 vs 9 (sequence ends at the first `m`) |
| width by grapheme (the span layout path) | `String.graphemes` + `Ucwidth.width/1` (first codepoint of the cluster) | 24/28. ZWJ, skin tone, Devanagari, Thai, stacked marks all correct. Misses, each by one cell: flag 7 vs 8, VS16 heart, keycap; tab |
| strip | `Utils.strip_escape_chars/1` | 27/28: only OSC 8 (`\e]8;;url\e\\` is cut at the `m` in `example.com`) |
| truncate, unstyled | `BackBreeze.String.truncate/2` | 25 to 28 of 28 per width. Every miss is the flag/VS16/tab width miss above; CJK boundaries, ZWJ, skin tone, combining marks all cut exactly where `x/ansi` cuts |
| truncate with `…` tail | emulated on top of `truncate/2` | same misses only |
| truncate, styled (SGR in the binary) | `BackBreeze.String.truncate/2` | fails on every styled fixture: it is ANSI-blind (`\e` counts as one cell), so `"\e[1mbold\e[0m plain"` at 6 gives `bo`. Breeze reaches this only for an SGR-bearing binary in an `overflow-hidden` box; idiomatic Breeze styles via spans/classes, where it does not apply |
| wrap, line for line | `BackBreeze.String.reflow/3` vs `ansi.Wrap` | 11 to 24 of 28 per width. Two systematic differences, neither a width bug: reflow keeps the trailing space on a wrapped line (`"hello "` vs `"hello"`); reflow does not break after `-` where `x/ansi` always does (`grapheme-aware` at 10: `grapheme-a` / `ware` vs `grapheme-` / `aware`). Long words hard-break at the width in both |
| wrap invariants (fits, keeps text) | reflow | fits: all but the over-count cases (ZWJ, skin tone, Devanagari), where reflow breaks a line early because it thinks the cluster is wider; keeps text: all but OSC 8 |
| wrap invariants, span path | `TextLayout.prepare/4` on `TextSpan`s | 28/28 fits, 28/28 keeps text. Note the span path breaks per grapheme, not per word |
| span truncate, `overflow: :hidden` | `TextLayout.prepare/4` | 25/28: the flag, VS16 heart and keycap rows let one extra cell through |

### What this means for the row renderers

- The Go list row (`internal/tui/list.go`) truncates and pads with
  `len([]rune)`, not cell width, so the bar to clear there is lower than
  `x/ansi`: BackBreeze's grapheme path is already strictly better than what
  ships. The `x/ansi` call sites proper are 8 in production (7 `Wrap` in
  detail/standup/runview/workflows, 1 `Truncate` in help) and the 181
  `ansi.Strip` plus 2 `ansi.StringWidth` calls in 13 test files; the "18
  call sites" in the task counted `lipgloss.Width` too (33 uses), which is
  the same primitive as `StringWidth`.
- Production impact of the misses: a task title carrying a flag, `⚠️`,
  `✔️`, `❤️` or a keycap renders one cell wider than measured, so the meta
  columns on that row shift right by one per such emoji. Tabs in a title
  render as one cell of the terminal's choosing. Nothing else in the
  fixture set misbehaves.
- The `Wrap` call sites wrap already-styled strings with word breaks.
  Breeze's span layout is grapheme-break only, and `reflow/3` is SGR-aware
  but a binary, so the port wraps text before styling it, or the vendored
  markdown renderer's `wrap_words` grows a span-aware version (an hour).
- `ansi.Strip` in tests maps to `Breeze.Test.render_text!/1`, which uses
  the same SGR-only stripper. Fine for Breeze's own output; it would not
  strip glamour-style OSC 8, which nothing in the port emits.

### Fail path: cost of a fix

`lib/spike4/gwidth.ex` is the patch, 15 lines: a cluster is 0 wide when it
is a lone tab, 2 wide when it contains U+FE0F or U+20E3 or is a
regional-indicator pair, otherwise `Ucwidth.width/1` of its first codepoint.
Against the reference: width 28/28, truncate 27/28. The one truncate miss
is `x/ansi` disagreeing with itself: `StringWidth("1️⃣ one")` is 6 but
`Truncate` cuts the keycap as one cell (`Truncate(s, 1, "")` returns
`"1️⃣"`), so there is no consistent Go behaviour to match there. Applying
it means `Ucwidth.width/1` taking the whole grapheme rather than
`String.next_codepoint`, and `Utils.string_length/1` and
`strip_escape_chars/1` learning the OSC terminator (`\a` or `\e\\`) as well
as `m`: under 30 lines in BackBreeze, a natural upstream PR, and cheap to
carry as a fork if it does not land, because the port would call these
through one `Tend.Width` module anyway.

## Not tested

Right-to-left text and bidi. Emoji with U+FE0E text presentation. Windows
terminals. Whether the terminal itself (Ghostty/iTerm/tmux) agrees with
`x/ansi` on flags and VS16, which is its own long-running disagreement.
Rendering speed of `Breeze.Markdown` on very long bodies (it is
`String.split` and regexes, so it will be fine). `Breeze.Markdown` at
widths under 20.
