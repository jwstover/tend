package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Quota is one reading of the account's subscription usage, as
// `claude -p "/usage"` reports it. SessionPct and WeekPct are zero both
// when usage is genuinely zero and when the line could not be read --
// check Unparsed to tell the two apart.
//
// It is never stored: a percentage is only meaningful next to its reset
// clock, and a stale row would be worse than no row at all.
type Quota struct {
	SessionPct    float64
	WeekPct       float64
	SessionResets time.Time
	WeekResets    time.Time
	// At is when this reading was taken.
	At time.Time
	// Unparsed holds the raw text of every "Current ..." line ParseQuota
	// could not fully make sense of -- a changed wording, an unparseable
	// percentage, or a reset clause whose timezone did not load. `/usage`
	// prints prose and claude is free to change it; a line landing here
	// leaves its field zero instead of a guess, so a format change
	// degrades to a blank indicator rather than a broken TUI.
	Unparsed []string
}

// quotaLine matches one limit line of `/usage`'s prose, e.g.
//
//	Current session: 48% used · resets Sep 15, 3:30pm (America/New_York)
//	Current week (all models): 6% used · resets Sep 17, 11pm (America/New_York)
//
// The reset clause is optional -- a limit can be reported at 0% used with
// nothing to reset -- and its own shape is parsed separately by
// parseResetTime, since it is the part most likely to drift.
var quotaLine = regexp.MustCompile(`^Current ([^:]+):\s*(\d+(?:\.\d+)?)%\s*used(?:\s*·\s*resets\s+(.+))?$`)

// resetClause splits a reset clause like "Sep 15, 3:30pm
// (America/New_York)" into its date-time part and IANA zone name.
var resetClause = regexp.MustCompile(`^(.+?)\s*\(([^()]+)\)$`)

// resetLayouts are the date-time shapes seen in captured `/usage` output.
// Claude omits the minutes when they are zero ("11pm" rather than
// "11:00pm"), so both are tried.
var resetLayouts = []string{
	"Jan 2, 3:04pm",
	"Jan 2, 3pm",
}

// ParseQuota reads the `result` text of a `claude -p --output-format
// json /usage` reply and picks the session and week-all-models limits out
// of it. It parses defensively: `/usage`'s output is prose meant for a
// human, not a stable API, so anything that does not fit the shape seen so
// far is recorded in Unparsed rather than failing the read. ParseQuota
// itself never errors -- there is nothing in free text that is fatal, only
// unrecognized -- the error return exists for symmetry with FetchQuota and
// to leave room for a future check that is fatal.
func ParseQuota(result string) (Quota, error) {
	q := Quota{At: time.Now()}
	for _, raw := range strings.Split(result, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "Current ") {
			continue
		}
		m := quotaLine.FindStringSubmatch(line)
		if m == nil {
			q.Unparsed = append(q.Unparsed, line)
			continue
		}
		pct, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			q.Unparsed = append(q.Unparsed, line)
			continue
		}
		var resets time.Time
		if clause := strings.TrimSpace(m[3]); clause != "" {
			var ok bool
			resets, ok = parseResetTime(clause)
			if !ok {
				q.Unparsed = append(q.Unparsed, line)
				continue
			}
		}
		switch m[1] {
		case "session":
			q.SessionPct, q.SessionResets = pct, resets
		case "week (all models)":
			q.WeekPct, q.WeekResets = pct, resets
		default:
			// A limit Phase 1 has no field for, such as a model-specific
			// weekly cap -- recorded rather than silently dropped.
			q.Unparsed = append(q.Unparsed, line)
		}
	}
	return q, nil
}

// parseResetTime parses a reset clause into a local, tz-aware time. It
// reports false rather than guess when the clause has no zone suffix, the
// zone does not load, or the date-time does not match a layout seen
// before -- callers keep the raw line in Unparsed instead.
//
// The clause carries no year, so one is inferred: this year, unless that
// falls more than a day in the past, in which case the reset is read as
// next year -- the only way a past-dated reset clause makes sense from a
// service that only ever reports limits resetting soon.
func parseResetTime(clause string) (time.Time, bool) {
	m := resetClause.FindStringSubmatch(clause)
	if m == nil {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(m[2])
	if err != nil {
		return time.Time{}, false
	}
	for _, layout := range resetLayouts {
		t, err := time.ParseInLocation(layout, m[1], loc)
		if err != nil {
			continue
		}
		now := time.Now().In(loc)
		t = t.AddDate(now.Year(), 0, 0)
		if t.Before(now.Add(-24 * time.Hour)) {
			t = t.AddDate(1, 0, 0)
		}
		return t, true
	}
	return time.Time{}, false
}

// quotaTimeout bounds one `/usage` call. It is a local command that
// answers in well under a second; anything past this is a wedged claude,
// not a slow one.
const quotaTimeout = 30 * time.Second

// runQuotaCmd runs `claude -p "/usage" --output-format json` and returns
// its raw stdout, behind a package-level var so FetchQuota can be tested
// without spawning claude -- the same seam runnerAlive is at
// internal/tui/runview.go:78.
//
// `/usage` reaches no model and is free, but print mode still behaves
// like an interactive session unless told not to:
//   - --no-session-persistence, or every poll writes a transcript under
//     ~/.claude/projects;
//   - --strict-mcp-config with no --mcp-config, or every poll starts the
//     user's configured MCP servers.
//
// It runs in a neutral directory so no project's hooks or settings apply.
var runQuotaCmd = func(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude",
		"-p", "--output-format", "json",
		"--no-session-persistence", "--strict-mcp-config",
		"/usage")
	cmd.Dir = "/tmp"
	return cmd.Output()
}

// usageWrapper is the subset of `claude -p --output-format json`'s reply
// FetchQuota reads.
type usageWrapper struct {
	LocalCommand string `json:"local_command"`
	IsError      bool   `json:"is_error"`
	Result       string `json:"result"`
}

// FetchQuota runs `/usage` and parses the result. Claude missing from
// $PATH, a non-zero exit, a reply that is not the expected wrapper, or a
// local_command other than "usage" are all reported as an error -- never
// panicked on -- and every one of them means the same thing to a caller:
// the reading is unknown, so an indicator built on it should go blank
// rather than show anything.
func FetchQuota(ctx context.Context) (Quota, error) {
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	out, err := runQuotaCmd(ctx)
	if err != nil {
		return Quota{}, fmt.Errorf("running claude /usage: %w", err)
	}
	var w usageWrapper
	if err := json.Unmarshal(out, &w); err != nil {
		return Quota{}, fmt.Errorf("decoding claude /usage output: %w", err)
	}
	if w.IsError {
		return Quota{}, fmt.Errorf("claude /usage failed: %s", strings.TrimSpace(w.Result))
	}
	if w.LocalCommand != "usage" {
		return Quota{}, fmt.Errorf("claude /usage: unexpected local_command %q", w.LocalCommand)
	}
	return ParseQuota(w.Result)
}
