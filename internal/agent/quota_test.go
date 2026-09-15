package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// usageJSON wraps result text in the shape `claude -p --output-format
// json /usage` prints, trimmed to the fields ParseQuota reads plus a few
// it must ignore.
func usageJSON(t *testing.T, isError bool, result string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type":          "result",
		"subtype":       "success",
		"is_error":      isError,
		"local_command": "usage",
		"num_turns":     0,
		"result":        result,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Captured from claude 2.1.272 on a subscription login.
const realUsage = `You are currently using your subscription to power your Claude Code usage

Current session: 48% used · resets Sep 15, 3:30pm (America/New_York)
Current week (all models): 6% used · resets Sep 17, 11pm (America/New_York)

What's contributing to your limits usage?
Approximate, based on local sessions on this machine — does not include other devices or claude.ai. Behaviors are independent characteristics, not a breakdown.

Last 24h · 163 requests · 7 sessions
  91% of your usage came from subagent-heavy sessions
  56% of your usage was at >150k context`

func TestParseQuota(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      string
		wantSession *QuotaLimit
		wantWeek    *QuotaLimit
		wantExtra   []QuotaLimit
	}{
		{
			name:        "real output",
			result:      realUsage,
			wantSession: &QuotaLimit{Label: "session", Percent: 48, Resets: "Sep 15, 3:30pm (America/New_York)"},
			wantWeek:    &QuotaLimit{Label: "week (all models)", Percent: 6, Resets: "Sep 17, 11pm (America/New_York)"},
		},
		{
			name:        "session only",
			result:      "Current session: 100% used · resets 4pm",
			wantSession: &QuotaLimit{Label: "session", Percent: 100, Resets: "4pm"},
		},
		{
			name:     "week only, no reset clause",
			result:   "Current week (all models): 0% used",
			wantWeek: &QuotaLimit{Label: "week (all models)", Percent: 0},
		},
		{
			name: "model-specific week goes to extra",
			result: "Current session: 3% used · resets 1pm\n" +
				"Current week (all models): 20% used · resets Fri\n" +
				"Current week (Sonnet only): 11% used · resets Fri",
			wantSession: &QuotaLimit{Label: "session", Percent: 3, Resets: "1pm"},
			wantWeek:    &QuotaLimit{Label: "week (all models)", Percent: 20, Resets: "Fri"},
			wantExtra:   []QuotaLimit{{Label: "week (Sonnet only)", Percent: 11, Resets: "Fri"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ParseQuota(usageJSON(t, false, tc.result))
			if err != nil {
				t.Fatalf("ParseQuota: %v", err)
			}
			if !limitEq(q.Session, tc.wantSession) {
				t.Errorf("Session = %+v, want %+v", q.Session, tc.wantSession)
			}
			if !limitEq(q.Week, tc.wantWeek) {
				t.Errorf("Week = %+v, want %+v", q.Week, tc.wantWeek)
			}
			if !slices.Equal(q.Extra, tc.wantExtra) {
				t.Errorf("Extra = %+v, want %+v", q.Extra, tc.wantExtra)
			}
		})
	}
}

func limitEq(a, b *QuotaLimit) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// An API-key login has no subscription limits; that is ErrNoQuota, which
// the header treats as "show nothing", not as a failure.
func TestParseQuotaNoLimits(t *testing.T) {
	_, err := ParseQuota(usageJSON(t, false, "You are currently using an API key to power your Claude Code usage"))
	if !errors.Is(err, ErrNoQuota) {
		t.Errorf("err = %v, want ErrNoQuota", err)
	}
}

func TestParseQuotaErrors(t *testing.T) {
	if _, err := ParseQuota(usageJSON(t, true, realUsage)); err == nil || errors.Is(err, ErrNoQuota) {
		t.Errorf("is_error result: err = %v, want a failure", err)
	}
	if _, err := ParseQuota([]byte("not json")); err == nil {
		t.Error("non-JSON output: want an error")
	}
}

// The two flags are what make a once-a-minute poll cheap: without them
// each call writes a transcript and starts every MCP server.
func TestQuotaCmdArgv(t *testing.T) {
	c := QuotaCmd(context.Background())
	want := []string{"-p", "--output-format", "json", "--no-session-persistence", "--strict-mcp-config", "/usage"}
	if got := c.Args[1:]; !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}
	if c.Dir == "" {
		t.Error("Dir is empty; want a neutral directory so no project's hooks apply")
	}
}
