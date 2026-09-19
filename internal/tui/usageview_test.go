package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/usage"
)

func usageFixture(now time.Time, projects int) []usage.Entry {
	var es []usage.Entry
	for i := range projects {
		es = append(es, usage.Entry{
			At: now.Add(-time.Hour), Model: "claude-opus-x", Cwd: fmt.Sprintf("/work/proj-%02d", i), SessionID: "s1",
			Tokens: usage.Tokens{Input: 100, CacheRead: 900, Output: 50},
		})
	}
	return es
}

func TestUsageViewRendersQuotaTotalsAndBreakdowns(t *testing.T) {
	m, _ := wideApp(t)
	now := time.Now()
	es := []usage.Entry{
		{At: now.Add(-time.Hour), Model: "claude-opus-x", Cwd: "/work/alpha", SessionID: "s1", Tokens: usage.Tokens{Input: 1000, CacheRead: 9000, Output: 500}},
		{At: now.Add(-48 * time.Hour), Model: "claude-haiku-y", Cwd: "/work/beta", SessionID: "s2", Sidechain: true, Tokens: usage.Tokens{Input: 200, Output: 100}},
		{At: now.Add(-30 * 24 * time.Hour), Model: "claude-opus-x", Cwd: "/work/beta", SessionID: "s3", Tokens: usage.Tokens{Output: 10}},
	}
	m = drive(t, m, usageMsgFor(es, 0, now))
	m = drive(t, m, quotaMsg{quota: agent.Quota{
		Session: &agent.QuotaLimit{Label: "session", Percent: 48, ResetsAt: now.Add(2 * time.Hour)},
		Week:    &agent.QuotaLimit{Label: "week (all models)", Percent: 6, Resets: "Sep 22, 9am (UTC)"},
	}})
	m = drive(t, m, keyPress('$'))
	if m.(app).mode != modeUsage {
		t.Fatalf("mode = %v, want modeUsage", m.(app).mode)
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"usage", "QUOTA", "48%", "resets in", "TOKENS", "all time", "BY PROJECT",
		"/work/alpha", "BY MODEL", "claude-opus-x", "claude-haiku-y", "MAIN VS SUB-AGENT", "sub-agent", "main"} {
		if !strings.Contains(content, want) {
			t.Errorf("usage view missing %q:\n%s", want, content)
		}
	}
	m = drive(t, m, esc())
	if m.(app).mode != modeList {
		t.Errorf("esc left mode %v, want list", m.(app).mode)
	}
}

func TestUsageViewBeforeFirstScan(t *testing.T) {
	m, _ := wideApp(t)
	m = drive(t, m, keyPress('$'))
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"scanning", "no subscription limits"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q:\n%s", want, content)
		}
	}
}

func TestUsageViewScrollClamps(t *testing.T) {
	m, _ := newTestApp(t)
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 14})
	m = drive(t, m, usageMsgFor(usageFixture(time.Now(), 30), 0, time.Now()))
	m = drive(t, m, keyPress('$'))
	maxS := m.(app).usageMaxScroll()
	if maxS == 0 {
		t.Fatal("fixture should overflow the pane")
	}
	m = drive(t, m, keyPress('G'))
	m = drive(t, m, keyPress('j'))
	if got := m.(app).uv.scroll; got != maxS {
		t.Errorf("scroll after G,j = %d, want %d", got, maxS)
	}
	m = drive(t, m, keyPress('k'))
	if got := m.(app).uv.scroll; got != maxS-1 {
		t.Errorf("scroll after k = %d, want %d", got, maxS-1)
	}
	m = drive(t, m, keyPress('g'))
	if got := m.(app).uv.scroll; got != 0 {
		t.Errorf("scroll after g = %d, want 0", got)
	}
	m = drive(t, m, keyPress('k'))
	if got := m.(app).uv.scroll; got != 0 {
		t.Errorf("scroll went negative: %d", got)
	}
}

func TestUsagePaletteEntry(t *testing.T) {
	m, _ := newTestApp(t)
	for _, c := range m.(app).paletteCommands() {
		if c.label != "Usage view" {
			continue
		}
		m, _ = c.act(m.(app))
		if m.(app).mode != modeUsage {
			t.Fatalf("mode = %v, want modeUsage", m.(app).mode)
		}
		return
	}
	t.Fatal("no Usage view palette command")
}

func TestFmtTokens(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 999: "999", 1000: "1.0k", 10_500: "10.5k", 99_949: "99.9k", 99_950: "100k", 999_499: "999k",
		999_500: "1.0M", 1_500_000: "1.5M", 12_400_000: "12.4M", 99_950_000: "100M", 999_999_999: "1.0B", 3_200_000_000: "3.2B",
	} {
		if got := fmtTokens(n); got != want {
			t.Errorf("fmtTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTruncHead(t *testing.T) {
	if got := truncHead("/a/b/c/project", 10, "…"); got != "…c/project" {
		t.Errorf("got %q", got)
	}
	if got := truncHead("short", 10, "…"); got != "short" {
		t.Errorf("got %q", got)
	}
}
