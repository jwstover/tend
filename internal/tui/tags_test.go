package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestFitTags(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		w    int
		want string
	}{
		{"one that fits", []string{"go"}, 12, "#go"},
		{"two that fit", []string{"go", "tui"}, 12, "#go #tui"},
		{"exactly full", []string{"go", "tui", "db"}, 12, "#go #tui #db"},
		// The case the old truncation handled badly: it produced
		// "#customer..." and lost both the tag and the count.
		{"long tags overflow to a counter",
			[]string{"support", "customer_PO", "urgent"}, 12, "#support +2"},
		{"two long tags", []string{"support", "customer_PO"}, 12, "#support +1"},
		{"several short ones", []string{"a", "b", "c", "d", "e", "f"}, 12, "#a #b #c +3"},
		// Narrower than one whole tag plus its counter: the tag gives way,
		// the counter survives, so the row still reports the remainder.
		{"tag truncated, counter kept", []string{"customer_PO", "urgent"}, 8, "#cus… +1"},
		{"single long tag truncates", []string{"customer_PO"}, 8, "#custom…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fitTags(tc.tags, tc.w, "…")
			if got != tc.want {
				t.Errorf("fitTags(%v, %d) = %q, want %q", tc.tags, tc.w, got, tc.want)
			}
		})
	}
}

// Whatever fitTags returns has to fit the column, or the meta block stops
// aligning down the list.
func TestFitTagsNeverOverflowsItsColumn(t *testing.T) {
	sets := [][]string{
		{"go"},
		{"go", "tui"},
		{"support", "customer_PO", "urgent"},
		{"a", "b", "c", "d", "e", "f", "g", "h"},
		{"an_extremely_long_single_tag_name"},
	}
	for _, w := range []int{4, 6, 8, 10, 12, 20} {
		for _, tags := range sets {
			got := fitTags(tags, w, "…")
			if runeWidth(got) > w {
				t.Errorf("fitTags(%v, %d) = %q, width %d exceeds the column",
					tags, w, got, runeWidth(got))
			}
		}
	}
}

// The list row shows tags; the detail pane shows all of them, however many.
func TestTagsRenderInRowAndDetail(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 16})

	tk, err := s.AddTask(ctx, "tagged task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetTags(ctx, tk.ID, []string{"support", "customer_PO", "urgent"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// Tags come back alphabetically, so customer_PO leads. At 12 columns
	// three tags cannot fit, and the counter is what makes that legible.
	row := ansi.Strip(m.View().Content)
	if !strings.Contains(row, "+2") {
		t.Errorf("row should report the two tags it could not fit:\n%s", row)
	}
	if strings.Contains(row, "#urgent") {
		t.Errorf("row should not have fitted every tag at 12 columns:\n%s", row)
	}

	m = drive(t, m, keyPress(']'))
	detail := ansi.Strip(m.View().Content)
	for _, want := range []string{"#support", "#customer_PO", "#urgent"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail pane missing %q:\n%s", want, detail)
		}
	}
}

// `/` matches tag text as well as titles, so a tag is a way to find a task
// even though filtering *by* tag is not its own view.
func TestSearchMatchesTagText(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	tagged, err := s.AddTask(ctx, "alpha")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddTask(ctx, "beta"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetTags(ctx, tagged.ID, []string{"errand"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// The tag is not in the title, so a match can only come from the tag.
	m = drive(t, m, keyPress('/'))
	for _, r := range "errand" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "alpha") {
		t.Errorf("searching a tag should find its task:\n%s", content)
	}
	if strings.Contains(content, "beta") {
		t.Errorf("searching a tag should exclude untagged tasks:\n%s", content)
	}
}

// Sub-tasks carry tags in the store, and the detail pane is where they are
// readable: the child row itself is deliberately meta-free (see
// renderChildRow), so this is the surface that has to show them.
func TestSubTaskTagsShowInItsDetailPane(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 16})

	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, parent.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if err := s.SetTags(ctx, child.ID, []string{"childtag"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('l')) // expand the branch
	m = drive(t, m, keyPress('j')) // onto the child
	m = drive(t, m, keyPress(']')) // open the pane on it

	if !strings.Contains(ansi.Strip(m.View().Content), "#childtag") {
		t.Errorf("a sub-task's tags should show in its detail pane:\n%s",
			ansi.Strip(m.View().Content))
	}
}
