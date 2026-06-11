package tui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	glamourstyles "charm.land/glamour/v2/styles"

	"github.com/jwstover/td/internal/task"
)

// newBodyRenderer builds a glamour renderer wrapped to the detail pane
// width. Glamour rendering is pure, so calling it inside Update is safe.
func newBodyRenderer(width int) (*glamour.TermRenderer, error) {
	if width < 10 {
		width = 10
	}
	return glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourstyles.DarkStyle),
		glamour.WithWordWrap(width),
	)
}

// renderDetail builds the full detail pane content: a compact metadata
// header, the glamour-rendered body, the sub-task checklist with progress,
// and the URLs detected in the body. selectedID highlights the checklist
// row of the sub-task under the list cursor (0 = none).
func renderDetail(t task.Task, children []task.Task, renderer *glamour.TermRenderer, styles Styles, selectedID int64) string {
	g := styles.Glyphs
	var b strings.Builder

	// Line 1: id + state (glyph, color, label — three signals).
	b.WriteString(" " + styles.DetailID.Render(fmt.Sprintf("#%d", t.ID)) + "  ")
	b.WriteString(styles.State[t.State].Bold(true).Render(g.State[t.State] + " " + string(t.State)))
	b.WriteString("\n")

	// Line 2: project · due · priority — only what's set.
	var meta []string
	if t.Project != nil {
		meta = append(meta, styles.DetailLabel.Render("project ")+styles.Dimmed.Render("#"+*t.Project))
	}
	if t.Due != nil {
		label, style := dueLabel(*t.Due, styles, time.Now())
		meta = append(meta, styles.DetailLabel.Render("due ")+style.Render(label))
	}
	if letter := task.PriorityLetter(t.Priority); letter != "" {
		meta = append(meta, styles.DetailLabel.Render("pri ")+styles.Priority[*t.Priority].Render(g.Flag+letter))
	}
	if len(meta) > 0 {
		b.WriteString("  " + strings.Join(meta, "   ") + "\n")
	}

	// Line 3: created / updated, faint.
	now := time.Now().UTC()
	b.WriteString("  " + styles.DetailFaint.Render(
		"created "+t.CreatedAt.Format("Jan 2")+" · updated "+relTime(t.UpdatedAt, now)) + "\n")

	if strings.TrimSpace(t.BodyMD) == "" {
		b.WriteString("\n  " + styles.Muted.Render("no body — ") +
			styles.FooterKey.Render("e") + styles.Muted.Render(" opens $EDITOR") + "\n")
	} else if renderer != nil {
		if out, err := renderer.Render(t.BodyMD); err == nil {
			b.WriteString(out)
		} else {
			b.WriteString("\n" + t.BodyMD + "\n")
		}
	} else {
		b.WriteString("\n" + t.BodyMD + "\n")
	}

	if urls := extractURLs(t.BodyMD); len(urls) > 0 {
		noun := "links"
		if len(urls) == 1 {
			noun = "link"
		}
		b.WriteString("\n" + "  " + styles.Link.Render(g.Link+" ") +
			styles.Muted.Render(fmt.Sprintf("%d %s detected — ", len(urls), noun)) +
			styles.FooterKey.Render("o") + styles.Muted.Render(" to open") + "\n")
		for i, u := range urls {
			b.WriteString("  " + styles.Muted.Render(fmt.Sprintf("[%d] ", i+1)) + styles.Link.Render(u) + "\n")
		}
	}

	if len(children) > 0 {
		done := 0
		for _, c := range children {
			if c.State == task.StateDone {
				done++
			}
		}
		count := styles.SubPartial
		if done == len(children) {
			count = styles.SubFull
		}
		b.WriteString("\n" + "  " + styles.SubHeader.Render("SUB-TASKS") + "  " +
			count.Render(fmt.Sprintf("%d/%d", done, len(children))) + "\n")
		for _, c := range children {
			title := styles.Title
			switch {
			case c.ID == selectedID:
				title = styles.SubSelText
			case c.State == task.StateDone:
				title = styles.SubDoneText
			}
			if c.State == task.StateDone {
				b.WriteString("  " + styles.CheckDone.Render(g.BoxChecked) + " " +
					title.Render(c.Title) + "\n")
			} else {
				b.WriteString("  " + styles.CheckOpen.Render(g.BoxUnchecked) + " " +
					title.Render(c.Title) + "\n")
			}
		}
	}

	return b.String()
}

// relTime renders an update timestamp as a short relative age, falling
// back to a date past a week.
func relTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}

var urlRe = regexp.MustCompile(`https?://[^\s<>()\[\]"']+`)

// extractURLs finds the unique URLs in a markdown body, in order of first
// appearance. TODO(owner): link-under-cursor selection instead of
// first/all.
func extractURLs(md string) []string {
	seen := map[string]bool{}
	var urls []string
	for _, u := range urlRe.FindAllString(md, -1) {
		u = strings.TrimRight(u, ".,;:")
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// openURLCmd opens a URL with the OS opener off the update loop.
func openURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		if err := exec.Command(opener, url).Run(); err != nil {
			return errMsg{fmt.Errorf("opening %s: %w", url, err)}
		}
		return statusMsg{kind: flashLink, text: "opened " + url}
	}
}

// editBodyCmd writes the body to a temp file and suspends the TUI to run
// $EDITOR on it; the callback message carries the file for reading back.
func editBodyCmd(t task.Task) tea.Cmd {
	f, err := os.CreateTemp("", fmt.Sprintf("td-%d-*.md", t.ID))
	if err != nil {
		return errCmd(fmt.Errorf("creating temp file: %w", err))
	}
	if _, err := f.WriteString(t.BodyMD); err != nil {
		f.Close()
		os.Remove(f.Name())
		return errCmd(fmt.Errorf("writing temp file: %w", err))
	}
	f.Close()

	parts := strings.Fields(editorCommand())
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	id := t.ID
	path := f.Name()
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorFinishedMsg{id: id, path: path, err: err}
	})
}

func editorCommand() string {
	if v := os.Getenv("VISUAL"); v != "" {
		return v
	}
	if v := os.Getenv("EDITOR"); v != "" {
		return v
	}
	return "vi"
}

func errCmd(err error) tea.Cmd {
	return func() tea.Msg { return errMsg{err} }
}
