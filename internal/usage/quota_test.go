package usage

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// realUsage is the `/usage` result text captured from claude 2.1.272 on a
// subscription login.
const realUsage = `You are currently using your subscription to power your Claude Code usage

Current session: 48% used · resets Sep 15, 3:30pm (America/New_York)
Current week (all models): 6% used · resets Sep 17, 11pm (America/New_York)

What's contributing to your limits usage?
Approximate, based on local sessions on this machine — does not include other devices or claude.ai. Behaviors are independent characteristics, not a breakdown.

Last 24h · 163 requests · 7 sessions
  91% of your usage came from subagent-heavy sessions
  56% of your usage was at >150k context`

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	return loc
}

func TestParseQuotaRealOutput(t *testing.T) {
	q, err := ParseQuota(realUsage)
	if err != nil {
		t.Fatalf("ParseQuota: %v", err)
	}
	if q.SessionPct != 48 {
		t.Errorf("SessionPct = %v, want 48", q.SessionPct)
	}
	if q.WeekPct != 6 {
		t.Errorf("WeekPct = %v, want 6", q.WeekPct)
	}
	if len(q.Unparsed) != 0 {
		t.Errorf("Unparsed = %v, want none", q.Unparsed)
	}

	ny := mustLoc(t, "America/New_York")
	wantSession := time.Date(time.Now().In(ny).Year(), time.September, 15, 15, 30, 0, 0, ny)
	if !q.SessionResets.Equal(wantSession) {
		t.Errorf("SessionResets = %v, want %v", q.SessionResets, wantSession)
	}
	wantWeek := time.Date(time.Now().In(ny).Year(), time.September, 17, 23, 0, 0, 0, ny)
	if !q.WeekResets.Equal(wantWeek) {
		t.Errorf("WeekResets = %v, want %v", q.WeekResets, wantWeek)
	}
}

func TestParseQuotaNoResetClause(t *testing.T) {
	q, err := ParseQuota("Current week (all models): 0% used")
	if err != nil {
		t.Fatalf("ParseQuota: %v", err)
	}
	if q.WeekPct != 0 {
		t.Errorf("WeekPct = %v, want 0", q.WeekPct)
	}
	if !q.WeekResets.IsZero() {
		t.Errorf("WeekResets = %v, want zero", q.WeekResets)
	}
	if len(q.Unparsed) != 0 {
		t.Errorf("Unparsed = %v, want none", q.Unparsed)
	}
}

func TestParseQuotaExtraLimit(t *testing.T) {
	result := "Current session: 3% used · resets Sep 15, 1pm (America/New_York)\n" +
		"Current week (Sonnet only): 11% used · resets Fri (America/New_York)"
	q, err := ParseQuota(result)
	if err != nil {
		t.Fatalf("ParseQuota: %v", err)
	}
	if q.SessionPct != 3 {
		t.Errorf("SessionPct = %v, want 3", q.SessionPct)
	}
	want := []string{"Current week (Sonnet only): 11% used · resets Fri (America/New_York)"}
	if !slices.Equal(q.Unparsed, want) {
		t.Errorf("Unparsed = %v, want %v", q.Unparsed, want)
	}
}

func TestParseQuotaChangedFormat(t *testing.T) {
	q, err := ParseQuota("Current session: forty-eight percent used, resets soon")
	if err != nil {
		t.Fatalf("ParseQuota: %v", err)
	}
	if q.SessionPct != 0 {
		t.Errorf("SessionPct = %v, want 0", q.SessionPct)
	}
	if len(q.Unparsed) != 1 {
		t.Fatalf("Unparsed = %v, want one entry", q.Unparsed)
	}
}

func TestParseQuotaBadZone(t *testing.T) {
	q, err := ParseQuota("Current session: 48% used · resets Sep 15, 3:30pm (Nowhere/Fake)")
	if err != nil {
		t.Fatalf("ParseQuota: %v", err)
	}
	if q.SessionPct != 0 || !q.SessionResets.IsZero() {
		t.Errorf("Session = %v/%v, want zero (unloadable zone)", q.SessionPct, q.SessionResets)
	}
	if len(q.Unparsed) != 1 {
		t.Fatalf("Unparsed = %v, want one entry", q.Unparsed)
	}
}

func TestParseQuotaTruncated(t *testing.T) {
	truncated := realUsage[:strings.LastIndex(realUsage, "(Ameri")+6]
	q, err := ParseQuota(truncated)
	if err != nil {
		t.Fatalf("ParseQuota: %v", err)
	}
	if q.SessionPct != 48 {
		t.Errorf("SessionPct = %v, want 48 (percent precedes the truncation)", q.SessionPct)
	}
	if !q.WeekResets.IsZero() {
		t.Errorf("WeekResets = %v, want zero: its line was cut off", q.WeekResets)
	}
	if len(q.Unparsed) != 1 {
		t.Errorf("Unparsed = %v, want the cut-off week line", q.Unparsed)
	}
}

func TestParseQuotaEmpty(t *testing.T) {
	q, err := ParseQuota("")
	if err != nil {
		t.Fatalf("ParseQuota: %v", err)
	}
	if q.SessionPct != 0 || q.WeekPct != 0 || len(q.Unparsed) != 0 {
		t.Errorf("ParseQuota(\"\") = %+v, want all zero", q)
	}
}

func TestFetchQuotaMissingClaude(t *testing.T) {
	orig := runQuotaCmd
	defer func() { runQuotaCmd = orig }()
	runQuotaCmd = func(ctx context.Context) ([]byte, error) {
		return nil, exec.ErrNotFound
	}
	if _, err := FetchQuota(context.Background()); err == nil {
		t.Error("FetchQuota with no claude on PATH: want an error, got nil")
	}
}

func TestFetchQuotaAssertsLocalCommand(t *testing.T) {
	orig := runQuotaCmd
	defer func() { runQuotaCmd = orig }()
	runQuotaCmd = func(ctx context.Context) ([]byte, error) {
		return []byte(`{"local_command":"cost","result":"not usage"}`), nil
	}
	if _, err := FetchQuota(context.Background()); err == nil {
		t.Error("FetchQuota with local_command != usage: want an error, got nil")
	}
}

func TestFetchQuotaParsesResult(t *testing.T) {
	orig := runQuotaCmd
	defer func() { runQuotaCmd = orig }()
	runQuotaCmd = func(ctx context.Context) ([]byte, error) {
		b, _ := json.Marshal(map[string]any{
			"local_command": "usage",
			"is_error":      false,
			"result":        realUsage,
		})
		return b, nil
	}
	q, err := FetchQuota(context.Background())
	if err != nil {
		t.Fatalf("FetchQuota: %v", err)
	}
	if q.SessionPct != 48 {
		t.Errorf("SessionPct = %v, want 48", q.SessionPct)
	}
}
