package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/settings"
)

// selectSetting presses j until the row for key is selected.
func selectSetting(t *testing.T, m tea.Model, key string) tea.Model {
	t.Helper()
	for range 10 {
		a := m.(app)
		if a.stv.rows[a.stv.cursor].entry.Key == key {
			return m
		}
		m = drive(t, m, keyPress('j'))
	}
	t.Fatalf("could not select %s", key)
	return m
}

func TestSettingsViewOpensAndLeaves(t *testing.T) {
	m, _ := newTestApp(t)
	m = drive(t, m, keyPress(','))
	a := m.(app)
	if a.mode != modeSettings || len(a.stv.rows) != len(settings.Entries()) {
		t.Fatalf("mode %v rows %d; want the settings view", a.mode, len(a.stv.rows))
	}
	view := ansi.Strip(a.View().Content)
	for _, want := range []string{"Diff viewer", "Notifications", "Diff tool", "Notification terminal", "com.mitchellh.ghostty (default)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	m = drive(t, m, esc())
	if m.(app).mode != modeList {
		t.Fatalf("esc did not return to the list, mode %v", m.(app).mode)
	}
	m = drive(t, m, keyPress(','))
	m = drive(t, m, keyPress(','))
	if m.(app).mode != modeList {
		t.Fatalf(", did not leave the view, mode %v", m.(app).mode)
	}
}

func TestSettingsViewOpensFromPalette(t *testing.T) {
	m, _ := newTestApp(t)
	m = drive(t, m, keyPress(':'))
	m = typeText(t, m, "Settings")
	m = drive(t, m, enter())
	if m.(app).mode != modeSettings {
		t.Fatalf("palette did not open the settings view, mode %v", m.(app).mode)
	}
}

func TestSettingsSetAndClearText(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m = drive(t, m, keyPress(','))
	m = selectSetting(t, m, settings.KeyNotifyActivate)
	m = drive(t, m, enter())
	if !m.(app).stv.editing || !m.(app).inputBusy() {
		t.Fatalf("enter did not focus the setting's input")
	}
	m = typeText(t, m, "com.apple.Terminal")
	m = drive(t, m, enter())
	waitFor(t, "setting stored", func() bool {
		v, err := s.NotifyActivateBundleID(ctx)
		return err == nil && v == "com.apple.Terminal"
	})
	if view := ansi.Strip(m.(app).View().Content); !strings.Contains(view, "com.apple.Terminal") {
		t.Errorf("row does not show the new value:\n%s", view)
	}

	// Editing again prefills the current value.
	m = drive(t, m, enter())
	if got := m.(app).stv.input.Value(); got != "com.apple.Terminal" {
		t.Errorf("input prefill = %q", got)
	}
	m = drive(t, m, esc())

	m = drive(t, m, keyPress('x'))
	waitFor(t, "setting cleared", func() bool {
		_, ok, err := s.Setting(ctx, settings.KeyNotifyActivate)
		return err == nil && !ok
	})
	if view := ansi.Strip(m.(app).View().Content); !strings.Contains(view, "com.mitchellh.ghostty (default)") {
		t.Errorf("row does not show the default again:\n%s", view)
	}
}

func TestSettingsRefusesInvalidDiffTool(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m = drive(t, m, keyPress(','))
	m = selectSetting(t, m, settings.KeyDiffTool)
	m = drive(t, m, enter())
	a := m.(app)
	if !a.stv.choosing {
		t.Fatal("enter on a choice row did not open the dropdown")
	}
	if view := ansi.Strip(a.View().Content); !strings.Contains(view, "auto (default)") || !strings.Contains(view, "custom…") {
		t.Errorf("option list lacks auto/custom:\n%s", view)
	}
	// Move to "custom…", the last option.
	for range 10 {
		m = drive(t, m, keyPress('j'))
	}
	m = drive(t, m, enter())
	if !m.(app).stv.editing {
		t.Fatal("custom… did not focus the input")
	}
	m = typeText(t, m, "difftool {bogus}")
	m = drive(t, m, enter())
	a = m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "{bogus}") {
		t.Errorf("status = %+v; want the validation message", a.status)
	}
	if !a.stv.editing || a.stv.input.Value() != "difftool {bogus}" {
		t.Error("a refused value should leave the input open on it")
	}
	if _, ok, _ := s.Setting(ctx, settings.KeyDiffTool); ok {
		t.Error("an invalid value was stored")
	}
}

func TestSettingsChoiceCustomValueStores(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m = drive(t, m, keyPress(','))
	m = selectSetting(t, m, settings.KeyDiffTool)
	m = drive(t, m, enter())
	for range 10 {
		m = drive(t, m, keyPress('j'))
	}
	m = drive(t, m, enter())
	m = typeText(t, m, "difftool {base} {head}")
	drive(t, m, enter())
	waitFor(t, "diff_tool stored", func() bool {
		v, ok, err := s.Setting(ctx, settings.KeyDiffTool)
		return err == nil && ok && v == "difftool {base} {head}"
	})
}

// Keys that mean something in the view type into a focused input.
func TestSettingsInputTakesViewKeys(t *testing.T) {
	m, _ := newTestApp(t)
	m = drive(t, m, keyPress(','))
	m = selectSetting(t, m, settings.KeyNotifyActivate)
	m = drive(t, m, enter())
	m = typeText(t, m, "qx,j?")
	a := m.(app)
	if a.mode != modeSettings || !a.stv.editing || a.helpOpen {
		t.Fatalf("mode %v editing %v help %v; want the input to keep focus", a.mode, a.stv.editing, a.helpOpen)
	}
	if got := a.stv.input.Value(); got != "qx,j?" {
		t.Errorf("input = %q", got)
	}
	m = drive(t, m, esc())
	if a := m.(app); a.stv.editing || a.mode != modeSettings {
		t.Fatalf("esc should cancel the edit and stay in the view (mode %v)", a.mode)
	}
}
