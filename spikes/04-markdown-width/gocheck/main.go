// gocheck is the Go side of spike 4: it produces the reference behaviour
// the Elixir side is compared against.
//
//	gocheck widths            JSON of x/ansi StringWidth/Truncate/Wrap over the fixture strings
//	gocheck glamour FILE W    FILE rendered by glamour exactly as internal/tui/detail.go does it
//
// Its own module so the root `go build ./...` and golangci-lint skip it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"charm.land/glamour/v2"
	glamourstyles "charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// fixture is one input string. The set covers what tend's rows and panes
// actually carry: plain ASCII, SGR-styled text, the glyph table in
// internal/tui/styles.go, East Asian wide characters, and the grapheme
// clusters that trip byte- or codepoint-based width code (combining
// marks, ZWJ sequences, flags, VS16 emoji presentation, keycaps, skin
// tones, Indic conjuncts, zero-width characters).
type fixture struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

var fixtures = []fixture{
	{"ascii", "hello world"},
	{"sgr_bold", "\x1b[1mbold\x1b[0m plain"},
	{"sgr_256_and_true_color", "\x1b[38;5;3m#tag\x1b[0m \x1b[38;2;255;100;0mdue\x1b[0m"},
	{"glyphs_state", "● ○ ◐ ◎ ⊘ ✓ ◇"},
	{"glyphs_session", "◌ ◉ ⊗ ⊙ ·"},
	{"glyphs_chrome", "▌▸▾▣▢⚑↗✚✎─│┬┴├┤…▰▱┌┐└┘◖ ◗"},
	{"styled_row", "\x1b[32m●\x1b[0m \x1b[1m▸\x1b[0m \x1b[36mSpike 4: Markdown\x1b[0m \x1b[38;5;3m#port\x1b[0m"},
	{"cjk", "日本語テキスト"},
	{"cjk_styled", "\x1b[36m日本語\x1b[0m text"},
	{"hangul", "한글 텍스트"},
	{"emoji_basic", "🍏 apple"},
	{"combining_nfd", "café résumé"},
	{"combining_stack", "à́̂b"},
	{"zwj_family", "👨‍👩‍👧 family"},
	{"flag", "🇯🇵 japan"},
	{"vs16_heart", "❤️ love"},
	{"text_presentation_heart", "❤ love"},
	{"keycap", "1️⃣ one"},
	{"skin_tone", "👍🏽 ok"},
	{"devanagari", "नमस्ते दुनिया"},
	{"thai", "สวัสดี"},
	{"zero_width_space", "a​b​c"},
	{"soft_hyphen", "co­op"},
	{"tab", "a\tb"},
	{"osc8_link", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ text"},
	{"long_word", "supercalifragilisticexpialidocious"},
	{"hyphenated", "grapheme-aware width-truncate-wrap"},
	{"mixed_paragraph", "Render 日本語 and 🍏 emoji with \x1b[1mbold\x1b[0m café, then wrap it."},
}

var truncWidths = []int{1, 2, 3, 4, 6, 8}
var wrapWidths = []int{4, 6, 10, 16}

type truncCase struct {
	W    int    `json:"w"`
	Tail string `json:"tail"`
	Out  string `json:"out"`
}

type wrapCase struct {
	W   int    `json:"w"`
	Out string `json:"out"`
}

type result struct {
	Name     string      `json:"name"`
	Input    string      `json:"input"`
	Strip    string      `json:"strip"`
	Width    int         `json:"width"`
	Truncate []truncCase `json:"truncate"`
	Wrap     []wrapCase  `json:"wrap"`
}

func widths() error {
	var out []result
	for _, f := range fixtures {
		r := result{
			Name:  f.Name,
			Input: f.Input,
			Strip: ansi.Strip(f.Input),
			Width: ansi.StringWidth(f.Input),
		}
		for _, w := range truncWidths {
			r.Truncate = append(r.Truncate,
				truncCase{w, "", ansi.Truncate(f.Input, w, "")},
				truncCase{w, "…", ansi.Truncate(f.Input, w, "…")},
			)
		}
		for _, w := range wrapWidths {
			r.Wrap = append(r.Wrap, wrapCase{w, ansi.Wrap(f.Input, w, "")})
		}
		out = append(out, r)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// glamourRender mirrors internal/tui/detail.go newBodyRenderer.
func glamourRender(path string, width int) error {
	md, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if width < 10 {
		width = 10
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourstyles.DarkStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return err
	}
	s, err := r.Render(string(md))
	if err != nil {
		return err
	}
	_, err = os.Stdout.WriteString(s)
	return err
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gocheck widths | gocheck glamour FILE WIDTH")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "widths":
		err = widths()
	case "glamour":
		if len(os.Args) != 4 {
			err = fmt.Errorf("glamour needs FILE and WIDTH")
			break
		}
		var w int
		w, err = strconv.Atoi(os.Args[3])
		if err == nil {
			err = glamourRender(os.Args[2], w)
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gocheck:", err)
		os.Exit(1)
	}
}
