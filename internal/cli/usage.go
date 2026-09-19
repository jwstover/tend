package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/usage"
)

// usageColW is the width of a number column in the text report.
const usageColW = 9

// usageDeps are the I/O seams of `tend usage`, so a test can run it
// against a temp transcript tree and a canned quota.
type usageDeps struct {
	root       func() (string, error)                     // usage.TranscriptRoot
	fetchQuota func(context.Context) (agent.Quota, error) // agent.FetchQuota; nil = --no-quota
	now        func() time.Time
}

func newUsageCmd() *cobra.Command {
	var asJSON, noQuota bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Print Claude token usage: quota, rolling totals and breakdowns",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := usageDeps{root: usage.TranscriptRoot, now: time.Now}
			if !noQuota {
				d.fetchQuota = agent.FetchQuota
			}
			return runUsage(cmd.Context(), cmd.OutOrStdout(), d, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"print machine-readable JSON with every token field, for diffing between runs")
	cmd.Flags().BoolVar(&noQuota, "no-quota", false,
		"skip the claude /usage call (no quota section; faster, and stable for diffs)")
	return cmd
}

type usageQuotaResult struct {
	q   agent.Quota
	err error
}

func runUsage(ctx context.Context, w io.Writer, d usageDeps, asJSON bool) error {
	var quotaCh chan usageQuotaResult
	if d.fetchQuota != nil {
		// Buffered so the goroutine never blocks if the scan fails first.
		quotaCh = make(chan usageQuotaResult, 1)
		go func() {
			q, err := d.fetchQuota(ctx)
			quotaCh <- usageQuotaResult{q, err}
		}()
	}

	root, err := d.root()
	if err != nil {
		return err
	}
	entries, skipped, err := usage.ScanTranscripts(root)
	if err != nil {
		return err
	}
	now := d.now()
	sum := usage.Summarize(entries, now)
	byProject := usage.Breakdown(entries, now, func(e usage.Entry) string { return orUnknown(e.Cwd) })
	byModel := usage.Breakdown(entries, now, func(e usage.Entry) string { return orUnknown(e.Model) })
	byAgent := usage.Breakdown(entries, now, func(e usage.Entry) string {
		if e.Sidechain {
			return "sub-agent"
		}
		return "main"
	})

	var quota *agent.Quota
	var quotaNote string
	if quotaCh != nil {
		r := <-quotaCh
		switch {
		case r.err == nil:
			quota = &r.q
		case errors.Is(r.err, agent.ErrNoQuota), errors.Is(r.err, exec.ErrNotFound):
			// Quiet degradation: no subscription limits to show.
		default:
			quotaNote = r.err.Error()
		}
	}

	if asJSON {
		return writeUsageJSON(w, now, quota, quotaNote, sum, byProject, byModel, byAgent, skipped)
	}
	writeUsageText(w, d.fetchQuota == nil, quota, quotaNote, sum, byProject, byModel, byAgent, skipped)
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// --- JSON ---

type usageTokensJSON struct {
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheCreation int64   `json:"cache_creation"`
	CacheRead     int64   `json:"cache_read"`
	Ephemeral5m   int64   `json:"ephemeral_5m"`
	Ephemeral1h   int64   `json:"ephemeral_1h"`
	InputTotal    int64   `json:"input_total"`
	Total         int64   `json:"total"`
	CacheHitRatio float64 `json:"cache_hit_ratio"`
}

type usageLimitJSON struct {
	Label    string     `json:"label"`
	Percent  int        `json:"percent"`
	Resets   string     `json:"resets,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

type usageQuotaJSON struct {
	Session *usageLimitJSON  `json:"session,omitempty"`
	Week    *usageLimitJSON  `json:"week,omitempty"`
	Extra   []usageLimitJSON `json:"extra,omitempty"`
}

type usageSpansJSON struct {
	Last5h  usageTokensJSON `json:"last_5h"`
	Last7d  usageTokensJSON `json:"last_7d"`
	AllTime usageTokensJSON `json:"all_time"`
}

type usageRowJSON struct {
	Key string `json:"key"`
	usageSpansJSON
}

type usageReportJSON struct {
	Now        time.Time       `json:"now"`
	Quota      *usageQuotaJSON `json:"quota"`
	QuotaError string          `json:"quota_error,omitempty"`
	Totals     usageSpansJSON  `json:"totals"`
	Window5h   struct {
		Messages int        `json:"messages"`
		Sessions int        `json:"sessions"`
		FirstAt  *time.Time `json:"first_at,omitempty"`
	} `json:"window_5h"`
	Breakdowns struct {
		Project []usageRowJSON `json:"project"`
		Model   []usageRowJSON `json:"model"`
		Agent   []usageRowJSON `json:"agent"`
	} `json:"breakdowns"`
	SkippedFiles int `json:"skipped_files"`
}

func tokensJSON(t usage.Tokens) usageTokensJSON {
	return usageTokensJSON{
		Input: t.Input, Output: t.Output,
		CacheCreation: t.CacheCreation, CacheRead: t.CacheRead,
		Ephemeral5m: t.Ephemeral5m, Ephemeral1h: t.Ephemeral1h,
		InputTotal: t.InputTotal(), Total: t.Total(), CacheHitRatio: t.CacheHitRatio(),
	}
}

func limitJSON(l *agent.QuotaLimit) *usageLimitJSON {
	if l == nil {
		return nil
	}
	out := &usageLimitJSON{Label: l.Label, Percent: l.Percent, Resets: l.Resets}
	if !l.ResetsAt.IsZero() {
		at := l.ResetsAt
		out.ResetsAt = &at
	}
	return out
}

func rowsJSON(rows []usage.BreakdownRow) []usageRowJSON {
	out := make([]usageRowJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, usageRowJSON{Key: r.Key, usageSpansJSON: usageSpansJSON{
			Last5h: tokensJSON(r.Last5h), Last7d: tokensJSON(r.Last7d), AllTime: tokensJSON(r.AllTime),
		}})
	}
	return out
}

func writeUsageJSON(w io.Writer, now time.Time, q *agent.Quota, quotaNote string, sum usage.Summary,
	byProject, byModel, byAgent []usage.BreakdownRow, skipped int) error {
	var rep usageReportJSON
	rep.Now = now
	if q != nil {
		rep.Quota = &usageQuotaJSON{Session: limitJSON(q.Session), Week: limitJSON(q.Week)}
		for i := range q.Extra {
			rep.Quota.Extra = append(rep.Quota.Extra, *limitJSON(&q.Extra[i]))
		}
	}
	rep.QuotaError = quotaNote
	rep.Totals = usageSpansJSON{Last5h: tokensJSON(sum.Last5h), Last7d: tokensJSON(sum.Last7d), AllTime: tokensJSON(sum.AllTime)}
	rep.Window5h.Messages = sum.Messages
	rep.Window5h.Sessions = sum.Sessions
	if !sum.FirstInWindow.IsZero() {
		at := sum.FirstInWindow
		rep.Window5h.FirstAt = &at
	}
	rep.Breakdowns.Project = rowsJSON(byProject)
	rep.Breakdowns.Model = rowsJSON(byModel)
	rep.Breakdowns.Agent = rowsJSON(byAgent)
	rep.SkippedFiles = skipped

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// --- text ---

func padRight(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func padLeft(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

func usageTildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home); ok {
			return "~" + rest
		}
	}
	return p
}

// usageLabelMax caps a project label in the text report so one deep cwd
// doesn't push the number columns off screen; JSON keeps the full path.
const usageLabelMax = 48

func usageTruncHead(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return "…" + string(r[len(r)-(w-1):])
}

func usageCacheHit(t usage.Tokens) string {
	if t.InputTotal() == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", t.CacheHitRatio()*100)
}

func usageCell(t usage.Tokens) string {
	if t.IsZero() {
		return "–"
	}
	return usage.FormatTokens(t.Total())
}

type usageTextRow struct {
	label string
	cells [3]string
}

func writeUsageText(w io.Writer, skippedQuota bool, q *agent.Quota, quotaNote string, sum usage.Summary,
	byProject, byModel, byAgent []usage.BreakdownRow, skipped int) {
	toRows := func(rows []usage.BreakdownRow, project bool) []usageTextRow {
		out := make([]usageTextRow, 0, len(rows))
		for _, r := range rows {
			label := r.Key
			if project {
				label = usageTruncHead(usageTildePath(label), usageLabelMax)
			}
			out = append(out, usageTextRow{label, [3]string{usageCell(r.Last5h), usageCell(r.Last7d), usageCell(r.AllTime)}})
		}
		return out
	}
	tokenRows := []usageTextRow{
		{"total", [3]string{usage.FormatTokens(sum.Last5h.Total()), usage.FormatTokens(sum.Last7d.Total()), usage.FormatTokens(sum.AllTime.Total())}},
		{"cache hit", [3]string{usageCacheHit(sum.Last5h), usageCacheHit(sum.Last7d), usageCacheHit(sum.AllTime)}},
		{"output", [3]string{usage.FormatTokens(sum.Last5h.Output), usage.FormatTokens(sum.Last7d.Output), usage.FormatTokens(sum.AllTime.Output)}},
	}
	sections := []struct {
		title string
		rows  []usageTextRow
	}{
		{"BY PROJECT", toRows(byProject, true)},
		{"BY MODEL", toRows(byModel, false)},
		{"MAIN VS SUB-AGENT", toRows(byAgent, false)},
	}

	labelW := 0
	widen := func(rows []usageTextRow) {
		for _, r := range rows {
			labelW = max(labelW, utf8.RuneCountInString(r.label))
		}
	}
	widen(tokenRows)
	for _, s := range sections {
		widen(s.rows)
	}
	header := func(title string) {
		fmt.Fprintf(w, "%s  %s  %s  %s\n", padRight(title, labelW+2),
			padLeft("5h", usageColW), padLeft("7d", usageColW), padLeft("all time", usageColW))
	}
	line := func(r usageTextRow) {
		fmt.Fprintf(w, "  %s  %s  %s  %s\n", padRight(r.label, labelW),
			padLeft(r.cells[0], usageColW), padLeft(r.cells[1], usageColW), padLeft(r.cells[2], usageColW))
	}

	fmt.Fprintln(w, "QUOTA")
	switch {
	case skippedQuota:
		fmt.Fprintln(w, "  skipped (--no-quota)")
	case quotaNote != "":
		fmt.Fprintf(w, "  quota unavailable: %s\n", quotaNote)
	case q == nil:
		fmt.Fprintln(w, "  no subscription limits reported (claude not installed, or an API-key login)")
	default:
		type qrow struct {
			label string
			l     agent.QuotaLimit
		}
		var rows []qrow
		if q.Session != nil {
			rows = append(rows, qrow{"5h", *q.Session})
		}
		if q.Week != nil {
			rows = append(rows, qrow{"wk", *q.Week})
		}
		for _, l := range q.Extra {
			rows = append(rows, qrow{l.Label, l})
		}
		if len(rows) == 0 {
			fmt.Fprintln(w, "  no subscription limits reported")
		}
		qw := 0
		for _, r := range rows {
			qw = max(qw, utf8.RuneCountInString(r.label))
		}
		for _, r := range rows {
			s := fmt.Sprintf("  %s  %3d%%", padRight(r.label, qw), r.l.Percent)
			if r.l.Resets != "" {
				s += "  resets " + r.l.Resets
			}
			fmt.Fprintln(w, s)
		}
	}

	fmt.Fprintln(w)
	header("TOKENS")
	for _, r := range tokenRows {
		line(r)
	}
	msg := fmt.Sprintf("  %d messages across %d sessions in the last 5h", sum.Messages, sum.Sessions)
	if !sum.FirstInWindow.IsZero() {
		msg += " · since " + strings.ToLower(sum.FirstInWindow.Local().Format("3:04PM"))
	}
	fmt.Fprintln(w, msg)

	for _, s := range sections {
		fmt.Fprintln(w)
		header(s.title)
		if len(s.rows) == 0 {
			fmt.Fprintln(w, "  none")
		}
		for _, r := range s.rows {
			line(r)
		}
	}
	if skipped > 0 {
		fmt.Fprintf(w, "\n%d transcript file(s) could not be read\n", skipped)
	}
}
