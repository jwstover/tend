// Package settings is the registry of user-facing preferences stored in the
// settings table. It is pure domain: no I/O, no store import. Internal state
// that shares the table (active_project_id) is deliberately not listed here.
//
// TODO(owner): tend settings ls/set/unset, sharing this registry's Validate.
// TODO(owner): TEND_GLYPHS is read at style init, before the DB is open, so
// moving it into settings is a separate decision.
package settings

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jwstover/tend/internal/vcs"
)

// Kind is how a setting is edited.
type Kind string

const (
	KindChoice Kind = "choice"
	KindText   Kind = "text"
	KindBool   Kind = "bool"
)

// Setting keys.
const (
	KeyDiffTool       = "diff_tool"
	KeyNotifyActivate = "notify_activate_bundle_id"
)

// ErrUnknownKey is returned for a key the registry does not list.
var ErrUnknownKey = errors.New("unknown setting")

// Entry describes one user-facing setting.
type Entry struct {
	Key         string
	Label       string
	Description string
	Kind        Kind
	// Default is the value in effect when the key is unset; DefaultNote says
	// what unset means when Default is empty.
	Default     string
	DefaultNote string
	// Choices are the fixed options of a KindChoice entry.
	Choices []string
	// AllowCustom lets a choice entry take a value outside Choices.
	AllowCustom bool
}

var entries = []Entry{
	{
		Key:         KeyDiffTool,
		Label:       "Diff tool",
		Description: "Full-screen tool for viewing a task's diff: a preset name or a command template using {base}, {head} and {repo}. External tools see refs only, so they show committed work only; uncommitted work is in the Diff tab.",
		Kind:        KindChoice,
		Default:     "",
		DefaultNote: "auto-detect: the first preset found on $PATH",
		Choices:     vcs.DiffToolPresetNames(),
		AllowCustom: true,
	},
	{
		Key:         KeyNotifyActivate,
		Label:       "Notification terminal",
		Description: "Bundle id of the terminal a notification click brings forward when it has no URL.",
		Kind:        KindText,
		// Same literal as notify.DefaultActivate; a test keeps them equal.
		Default: "com.mitchellh.ghostty",
	},
}

// Entries returns the registry in display order.
func Entries() []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// Lookup finds an entry by key.
func Lookup(key string) (Entry, bool) {
	for _, e := range entries {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

var placeholderRe = regexp.MustCompile(`\{[^{}]*\}`)

var allowedPlaceholders = map[string]bool{"{base}": true, "{head}": true, "{repo}": true}

// Validate checks value against the entry for key.
func Validate(key, value string) error {
	e, ok := Lookup(key)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	switch e.Kind {
	case KindBool:
		if value != "true" && value != "false" {
			return fmt.Errorf("%s: want true or false, got %q", key, value)
		}
		return nil
	case KindChoice:
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s: value is empty", key)
		}
		for _, c := range e.Choices {
			if c == value {
				return nil
			}
		}
		if !e.AllowCustom {
			return fmt.Errorf("%s: %q is not one of %s", key, value, strings.Join(e.Choices, ", "))
		}
	case KindText:
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s: value is empty", key)
		}
	}
	if key == KeyDiffTool {
		for _, p := range placeholderRe.FindAllString(value, -1) {
			if !allowedPlaceholders[p] {
				return fmt.Errorf("%s: unknown placeholder %s (allowed: {base}, {head}, {repo})", key, p)
			}
		}
	}
	return nil
}
