package tui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

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

// renderDetail builds the full detail pane content: glamour-rendered body,
// sub-task checklist with progress, and the URLs detected in the body.
func renderDetail(t task.Task, children []task.Task, renderer *glamour.TermRenderer, styles Styles) string {
	var b strings.Builder

	b.WriteString(styles.DetailTitle.Render(fmt.Sprintf("#%d %s", t.ID, t.Title)))
	b.WriteString("\n")
	meta := string(t.State)
	if t.Project != nil {
		meta += "  @" + *t.Project
	}
	if t.Due != nil {
		meta += "  due:" + *t.Due
	}
	b.WriteString(styles.Dimmed.Render(meta))
	b.WriteString("\n")

	if strings.TrimSpace(t.BodyMD) == "" {
		b.WriteString("\n" + styles.Dimmed.Render("no body — press e to write one") + "\n")
	} else if renderer != nil {
		if out, err := renderer.Render(t.BodyMD); err == nil {
			b.WriteString(out)
		} else {
			b.WriteString("\n" + t.BodyMD + "\n")
		}
	} else {
		b.WriteString("\n" + t.BodyMD + "\n")
	}

	if len(children) > 0 {
		done := 0
		for _, c := range children {
			if c.State == task.StateDone {
				done++
			}
		}
		b.WriteString("\n" + styles.DetailTitle.Render(fmt.Sprintf("sub-tasks %d/%d", done, len(children))) + "\n")
		for _, c := range children {
			box := "[ ]"
			style := styles.Normal
			if c.State == task.StateDone {
				box = "[x]"
				style = styles.Dimmed
			}
			b.WriteString(style.Render(fmt.Sprintf("%s %s", box, c.Title)) + "\n")
		}
	}

	if urls := extractURLs(t.BodyMD); len(urls) > 0 {
		b.WriteString("\n" + styles.DetailTitle.Render("links") + "\n")
		for i, u := range urls {
			b.WriteString(styles.Dimmed.Render(fmt.Sprintf("[%d] ", i+1)) + u + "\n")
		}
	}

	return b.String()
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
		return statusMsg("opened " + url)
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
