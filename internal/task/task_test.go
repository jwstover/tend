package task

import (
	"errors"
	"testing"
)

func TestNormalizeDate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "valid", in: "2026-06-09", want: "2026-06-09"},
		{name: "trims whitespace", in: " 2026-06-09 ", want: "2026-06-09"},
		{name: "wrong format", in: "06/09/2026", wantErr: true},
		{name: "not a date", in: "tomorrow", wantErr: true},
		{name: "impossible date", in: "2026-02-30", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeDate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeDate(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeDate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStateValid(t *testing.T) {
	for _, s := range []State{StateInbox, StateTodo, StateDoing, StateBlocked, StateDone, StateSomeday} {
		if !s.Valid() {
			t.Errorf("State(%q).Valid() = false, want true", s)
		}
	}
	for _, s := range []State{"", "DONE", "archived"} {
		if s.Valid() {
			t.Errorf("State(%q).Valid() = true, want false", s)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "plain", in: "buy milk", want: "buy milk"},
		{name: "trims whitespace", in: "  buy milk\n", want: "buy milk"},
		{name: "interior whitespace kept", in: "a  b", want: "a  b"},
		{name: "empty", in: "", wantErr: ErrEmptyTitle},
		{name: "only whitespace", in: " \t\n", wantErr: ErrEmptyTitle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeTitle(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeTitle(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
