package task

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty is no tags", "", nil},
		{"whitespace only", "   \t ", nil},
		{"single", "work", []string{"work"}},
		{"space separated", "work home", []string{"work", "home"}},
		{"comma separated", "work,home", []string{"work", "home"}},
		{"comma and space mixed", "work, home,  errands", []string{"work", "home", "errands"}},
		{"leading hashes are display sugar", "#work #home", []string{"work", "home"}},
		{"surrounding whitespace trimmed", "  work  ,  home  ", []string{"work", "home"}},
		{"duplicates dropped", "work work", []string{"work"}},
		{"duplicates folded case-insensitively, first spelling wins",
			"Work work WORK", []string{"Work"}},
		{"a bare hash is not a tag", "# work", []string{"work"}},
		{"case preserved as typed", "Work", []string{"Work"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTags(tc.in)
			// Never nil: a cleared prompt is an explicit "no tags", which
			// SetTags relies on to distinguish clear from absent.
			if got == nil {
				t.Fatal("ParseTags returned nil, want an empty slice")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseTags(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ParseTags(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The prompt seeds from FormatTags and submits through ParseTags, so
// opening it and pressing enter unchanged must be a no-op.
func TestFormatTagsRoundTripsThroughParseTags(t *testing.T) {
	original := []string{"alpha", "beta", "gamma"}
	got := ParseTags(FormatTags(original))
	if strings.Join(got, " ") != strings.Join(original, " ") {
		t.Errorf("round trip = %v, want %v", got, original)
	}
}

func TestNormalizeTag(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"work", "work", true},
		{"#work", "work", true},
		{"  #work  ", "work", true},
		{"# work", "work", true},
		{"", "", false},
		{"   ", "", false},
		{"#", "", false},
		{"  #  ", "", false},
	}
	for _, tc := range tests {
		got, ok := NormalizeTag(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("NormalizeTag(%q) = (%q, %v), want (%q, %v)",
				tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestNormalizeProjectName(t *testing.T) {
	got, err := NormalizeProjectName("  tend  ")
	if err != nil || got != "tend" {
		t.Errorf("NormalizeProjectName = (%q, %v), want (tend, nil)", got, err)
	}
	if _, err := NormalizeProjectName("   "); !errors.Is(err, ErrEmptyProjectName) {
		t.Errorf("NormalizeProjectName(blank) = %v, want ErrEmptyProjectName", err)
	}
}
