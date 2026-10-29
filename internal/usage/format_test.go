package usage

import "testing"

func TestFormatTokens(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 999: "999", 1000: "1.0k", 10_500: "10.5k", 99_949: "99.9k", 99_950: "100k", 999_499: "999k",
		999_500: "1.0M", 1_500_000: "1.5M", 12_400_000: "12.4M", 99_950_000: "100M", 999_999_999: "1.0B", 3_200_000_000: "3.2B",
	} {
		if got := FormatTokens(n); got != want {
			t.Errorf("FormatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}
