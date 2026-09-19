package usage

import "fmt"

// FormatTokens abbreviates a token count: 950, 1.2k, 45k, 1.5M, 3.2B. The
// one decimal is kept below 100 of a unit, and the unit is chosen after
// rounding so 999_950 reads 1.0M, never 1000k.
//
// TODO(owner): internal/tui's fmtTokens duplicates this; switch it over once the in-flight tui edits land.
func FormatTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 99_950:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 999_500:
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n < 99_950_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n < 999_500_000:
		return fmt.Sprintf("%dM", (n+500_000)/1_000_000)
	default:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	}
}
