package task

import (
	"errors"
	"testing"
)

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
