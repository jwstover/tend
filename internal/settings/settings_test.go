package settings

import (
	"errors"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/notify"
)

func TestEntries(t *testing.T) {
	es := Entries()
	if len(es) != 2 || es[0].Key != "diff_tool" || es[1].Key != "notify_activate_bundle_id" {
		t.Fatalf("entries = %+v", es)
	}
	for _, e := range es {
		if e.Label == "" || e.Description == "" || e.Kind == "" {
			t.Errorf("incomplete entry %+v", e)
		}
	}
	if _, ok := Lookup("active_project_id"); ok {
		t.Error("internal key must not be in the registry")
	}
}

func TestNotifyDefaultMatches(t *testing.T) {
	e, _ := Lookup(KeyNotifyActivate)
	if e.Default != notify.DefaultActivate {
		t.Errorf("default %q != notify.DefaultActivate %q", e.Default, notify.DefaultActivate)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name, key, value, errHas string
	}{
		{"bogus placeholder", KeyDiffTool, "git diff {base}...{head} {bogus}", "{bogus}"},
		{"valid template", KeyDiffTool, "git -C {repo} diff {base}...{head}", ""},
		{"bare name", KeyDiffTool, "delta", ""},
		{"empty diff tool", KeyDiffTool, " ", "empty"},
		{"text ok", KeyNotifyActivate, "com.apple.Terminal", ""},
		{"text empty", KeyNotifyActivate, "", "empty"},
		{"unknown key", "nope", "x", "unknown setting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.key, tt.value)
			if tt.errHas == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errHas) {
				t.Fatalf("err = %v, want containing %q", err, tt.errHas)
			}
		})
	}
	if !errors.Is(Validate("nope", "x"), ErrUnknownKey) {
		t.Error("want ErrUnknownKey")
	}
}

func TestDiffToolOffersEveryPreset(t *testing.T) {
	e, _ := Lookup(KeyDiffTool)
	want := []string{"delta", "difftastic", "tig", "diffnav", "diffview", "lazygit", "but", "less"}
	if len(e.Choices) != len(want) {
		t.Fatalf("choices = %v", e.Choices)
	}
	for i, c := range want {
		if e.Choices[i] != c {
			t.Fatalf("choices = %v", e.Choices)
		}
	}
	if !strings.Contains(e.Description, "committed work only") {
		t.Errorf("description should say committed work only: %q", e.Description)
	}
}
