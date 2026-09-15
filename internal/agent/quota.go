package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// QuotaLimit is one of the subscription limits `/usage` reports: how much
// of it is spent and, as claude words it, when it resets. Resets is kept
// as claude's own text ("Sep 15, 3:30pm (America/New_York)") rather than
// parsed, since nothing needs it as a time yet and its format is claude's
// to change.
type QuotaLimit struct {
	Label   string
	Percent int
	Resets  string
}

// Quota is the account's subscription usage as `/usage` reports it.
// Session is the rolling five-hour window claude calls "Current session",
// Week the all-models weekly limit. Either is nil when claude did not
// report it, so a view drops that gauge rather than inventing a zero.
// Extra holds any other "Current …" line, such as a model-specific
// weekly limit.
type Quota struct {
	Session *QuotaLimit
	Week    *QuotaLimit
	Extra   []QuotaLimit
}

// ErrNoQuota is returned by ParseQuota when claude answered but reported
// no limits at all -- an API-key login, which has no subscription quota,
// or a `/usage` whose wording has changed. Callers show nothing for it
// rather than an error.
var ErrNoQuota = errors.New("claude /usage reported no subscription limits")

// quotaTimeout bounds one `/usage` call. It is a local command that
// answers in under a second; anything past this is a wedged claude, not a
// slow one.
const quotaTimeout = 30 * time.Second

// QuotaCmd builds the `claude -p /usage` call FetchQuota runs.
//
// `/usage` is a local command: it reaches no model and costs nothing. But
// print mode still behaves like a session unless told not to, and at a
// once-a-minute poll both defaults are expensive:
//   - --no-session-persistence, or every call writes a transcript under
//     ~/.claude/projects -- about 1,440 empty sessions a day;
//   - --strict-mcp-config with no --mcp-config, or every call starts the
//     user's MCP servers, `tend mcp` among them.
//
// It runs in the temp directory so no project's hooks or settings apply.
func QuotaCmd(ctx context.Context) *exec.Cmd {
	c := exec.CommandContext(ctx, binary,
		"-p", "--output-format", "json",
		"--no-session-persistence", "--strict-mcp-config",
		"/usage")
	c.Dir = os.TempDir()
	// A hook claude spawns could inherit stdout and hold the pipe open
	// past a timeout; stop waiting on it shortly after the kill.
	c.WaitDelay = 2 * time.Second
	return c
}

// FetchQuota runs QuotaCmd and parses what it prints.
func FetchQuota(ctx context.Context) (Quota, error) {
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	out, err := QuotaCmd(ctx).Output()
	if err != nil {
		return Quota{}, fmt.Errorf("running claude /usage: %w", err)
	}
	return ParseQuota(out)
}

// quotaLine matches one limit line of `/usage`'s text, e.g.
//
//	Current session: 48% used · resets Sep 15, 3:30pm (America/New_York)
var quotaLine = regexp.MustCompile(`^Current ([^:]+):\s*(\d+)% used(?:\s*·\s*resets\s+(.+))?$`)

// ParseQuota reads `claude -p --output-format json /usage` output: the
// result event's text carries the limits, one "Current …" line each.
func ParseQuota(out []byte) (Quota, error) {
	var res struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return Quota{}, fmt.Errorf("parsing claude /usage output: %w", err)
	}
	if res.IsError {
		return Quota{}, fmt.Errorf("claude /usage failed: %s", strings.TrimSpace(res.Result))
	}
	var q Quota
	for _, line := range strings.Split(res.Result, "\n") {
		m := quotaLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		pct, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		l := QuotaLimit{Label: m[1], Percent: pct, Resets: strings.TrimSpace(m[3])}
		switch {
		case l.Label == "session" && q.Session == nil:
			q.Session = &l
		case l.Label == "week (all models)" && q.Week == nil:
			q.Week = &l
		default:
			q.Extra = append(q.Extra, l)
		}
	}
	if q.Session == nil && q.Week == nil && len(q.Extra) == 0 {
		return Quota{}, ErrNoQuota
	}
	return q, nil
}
