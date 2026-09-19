package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/usage"
)

var usageNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func usageLine(id, cwd, model string, side bool, at time.Time, in, out, cc, cr int) string {
	return fmt.Sprintf(`{"isSidechain":%t,"cwd":%q,"sessionId":"s-%s","type":"assistant","timestamp":%q,"message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation":{"ephemeral_1h_input_tokens":%d,"ephemeral_5m_input_tokens":0}}}}`,
		side, cwd, id, at.Format(time.RFC3339), id, model, in, out, cc, cr, cc)
}

// usageFixture writes a transcript tree and returns its root.
func usageFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		usageLine("m1", "/w/a", "model-a", false, usageNow.Add(-time.Hour), 10, 20, 30, 40),
		usageLine("m2", "/w/b", "model-b", false, usageNow.Add(-48*time.Hour), 1, 2, 3, 4),
		usageLine("m3", "/w/a", "model-a", true, usageNow.Add(-30*24*time.Hour), 100, 200, 300, 400),
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func usageDepsFor(root string, q func(context.Context) (agent.Quota, error)) usageDeps {
	return usageDeps{
		root:       func() (string, error) { return root, nil },
		fetchQuota: q,
		now:        func() time.Time { return usageNow },
	}
}

func cannedQuota(context.Context) (agent.Quota, error) {
	return agent.Quota{Session: &agent.QuotaLimit{Label: "session", Percent: 48, Resets: "Sep 19, 3:30pm (America/New_York)"}}, nil
}

func TestUsageJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := runUsage(context.Background(), &buf, usageDepsFor(usageFixture(t), cannedQuota), true); err != nil {
		t.Fatal(err)
	}
	var rep usageReportJSON
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if got := rep.Totals.Last5h; got.Input != 10 || got.Output != 20 || got.CacheCreation != 30 || got.CacheRead != 40 || got.Ephemeral1h != 30 {
		t.Errorf("5h = %+v", got)
	}
	if got := rep.Totals.Last7d; got.Input != 11 || got.Output != 22 || got.CacheCreation != 33 || got.CacheRead != 44 {
		t.Errorf("7d = %+v", got)
	}
	if got := rep.Totals.AllTime; got.Input != 111 || got.Output != 222 || got.CacheCreation != 333 || got.CacheRead != 444 || got.Total != 111+222+333+444 {
		t.Errorf("all = %+v", got)
	}
	if rep.Quota == nil || rep.Quota.Session == nil || rep.Quota.Session.Percent != 48 {
		t.Errorf("quota = %+v", rep.Quota)
	}
	keys := func(rows []usageRowJSON) map[string]bool {
		m := map[string]bool{}
		for _, r := range rows {
			m[r.Key] = true
		}
		return m
	}
	if p := keys(rep.Breakdowns.Project); !p["/w/a"] || !p["/w/b"] {
		t.Errorf("projects = %v", p)
	}
	if m := keys(rep.Breakdowns.Model); !m["model-a"] || !m["model-b"] {
		t.Errorf("models = %v", m)
	}
	if a := keys(rep.Breakdowns.Agent); !a["main"] || !a["sub-agent"] {
		t.Errorf("agents = %v", a)
	}
}

func TestUsageEmpty(t *testing.T) {
	var buf bytes.Buffer
	d := usageDepsFor(filepath.Join(t.TempDir(), "nope"), nil)
	if err := runUsage(context.Background(), &buf, d, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"project": []`) {
		t.Errorf("want empty arrays, got %s", buf.String())
	}
	var rep usageReportJSON
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Totals.AllTime.Total != 0 || rep.Quota != nil {
		t.Errorf("rep = %+v", rep)
	}
	buf.Reset()
	if err := runUsage(context.Background(), &buf, d, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "none") {
		t.Errorf("text = %s", buf.String())
	}
}

func TestUsageQuotaDegrades(t *testing.T) {
	root := usageFixture(t)
	fail := func(err error) func(context.Context) (agent.Quota, error) {
		return func(context.Context) (agent.Quota, error) { return agent.Quota{}, err }
	}
	tests := []struct {
		name     string
		fetch    func(context.Context) (agent.Quota, error)
		wantText string
		wantErr  bool
	}{
		{"no quota", fail(agent.ErrNoQuota), "no subscription limits reported", false},
		{"no claude", fail(fmt.Errorf("run: %w", exec.ErrNotFound)), "no subscription limits reported", false},
		{"other", fail(errors.New("boom")), "quota unavailable: boom", true},
		{"skipped", nil, "skipped (--no-quota)", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var text, js bytes.Buffer
			d := usageDepsFor(root, tc.fetch)
			if err := runUsage(context.Background(), &text, d, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text.String(), tc.wantText) {
				t.Errorf("text missing %q:\n%s", tc.wantText, text.String())
			}
			if err := runUsage(context.Background(), &js, d, true); err != nil {
				t.Fatal(err)
			}
			var rep usageReportJSON
			if err := json.Unmarshal(js.Bytes(), &rep); err != nil {
				t.Fatal(err)
			}
			if rep.Quota != nil || (rep.QuotaError != "") != tc.wantErr {
				t.Errorf("quota=%+v err=%q", rep.Quota, rep.QuotaError)
			}
		})
	}
}

func TestUsageText(t *testing.T) {
	var buf bytes.Buffer
	if err := runUsage(context.Background(), &buf, usageDepsFor(usageFixture(t), cannedQuota), false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, h := range []string{"QUOTA", "TOKENS", "BY PROJECT", "BY MODEL", "MAIN VS SUB-AGENT", "48%", "sub-agent"} {
		if !strings.Contains(out, h) {
			t.Errorf("missing %q:\n%s", h, out)
		}
	}
	want := usage.FormatTokens(111 + 222 + 333 + 444)
	found := false
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "total") && strings.HasSuffix(l, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("no total line ending %q:\n%s", want, out)
	}
}

func TestUsageCommandSmoke(t *testing.T) {
	cfg := t.TempDir()
	if err := os.Rename(usageFixture(t), filepath.Join(cfg, "projects")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	cmd := newUsageCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--no-quota", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rep usageReportJSON
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Totals.AllTime.Input != 111 {
		t.Errorf("all = %+v", rep.Totals.AllTime)
	}
}
