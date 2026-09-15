// Package usage reads the token accounting claude records and rolls it up
// the ways tend needs to show it: per step run, per project, and over the
// rolling windows an account's limits are measured in.
//
// There are two sources and they carry the same numbers in the same shape:
//
//   - a workflow step's stream-json log (agent.StepLogPath), written by
//     `claude -p --output-format stream-json`; and
//   - claude's own session transcripts under ~/.claude/projects/<slug>/
//     <session-id>.jsonl, which cover every session on the machine,
//     tend-launched or not.
//
// Both write one JSON object per line and both put a message's usage at
// message.usage on an `assistant` line, so one parser serves both (see
// ParseLine). That matters for the machine-wide view: tend's own runs are
// already in the transcripts, so scanning ~/.claude/projects alone gives a
// complete picture without double counting, and the step logs are only
// needed to attribute usage back to a specific step run.
package usage

import "time"

// Tokens is the token accounting of one or more assistant messages. The
// four fields are the ones claude reports and the ones an account's
// limits are counted against; Ephemeral5m and Ephemeral1h are the
// cache-creation total split by the TTL it was written at, which is how
// tend can tell whether a workflow is getting the 5-minute or the 1-hour
// prompt cache (tend task #21).
//
// The zero value is a usable empty total.
type Tokens struct {
	Input         int64
	Output        int64
	CacheCreation int64
	CacheRead     int64
	Ephemeral5m   int64
	Ephemeral1h   int64
}

// Add returns t plus o. Tokens is a value type and Add does not mutate,
// so a running total is `total = total.Add(next)`.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		Input:         t.Input + o.Input,
		Output:        t.Output + o.Output,
		CacheCreation: t.CacheCreation + o.CacheCreation,
		CacheRead:     t.CacheRead + o.CacheRead,
		Ephemeral5m:   t.Ephemeral5m + o.Ephemeral5m,
		Ephemeral1h:   t.Ephemeral1h + o.Ephemeral1h,
	}
}

// InputTotal is every token that entered the model: fresh input, tokens
// written to the prompt cache, and tokens read back from it. This is the
// denominator CacheHitRatio reports against, and the number to compare
// against a context window -- not Input alone, which is only the part
// that was neither cached nor cacheable and is near zero in a long
// session.
func (t Tokens) InputTotal() int64 { return t.Input + t.CacheCreation + t.CacheRead }

// Total is every token in and out. Rolling-window limits are counted
// against something close to this, so it is what the usage view trends.
func (t Tokens) Total() int64 { return t.InputTotal() + t.Output }

// CacheHitRatio is the share of input served from the prompt cache, 0
// when nothing entered the model at all. This is the number the workflow
// measurement work turns on: a step that re-reads what the previous step
// already read shows up as a low ratio on a large InputTotal, and a step
// continuing a warm session shows up as a high one.
func (t Tokens) CacheHitRatio() float64 {
	in := t.InputTotal()
	if in == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(in)
}

// IsZero reports whether nothing was counted, so callers can skip a step
// run or a session that never reached the model.
func (t Tokens) IsZero() bool { return t == Tokens{} }

// Entry is one assistant message's usage with the attribution a rollup
// needs. MessageID is claude's own id for the message and is what
// deduplicates: the same message is written to a transcript more than
// once when a session is resumed or forked, and counting it twice would
// overstate usage by however much of the history was replayed.
//
// Sidechain marks a message from a sub-agent rather than the main thread.
// It still costs tokens and still counts against a limit, so rollups
// include it by default; the flag is kept so a breakdown can separate it.
type Entry struct {
	Tokens
	At        time.Time
	Model     string
	SessionID string
	MessageID string
	// Cwd is the working directory claude recorded for the session, which
	// is how a transcript is attributed to a project. Empty in a step log,
	// where the step run already names the run and its task.
	Cwd       string
	Sidechain bool
}

// Sum totals every entry.
func Sum(entries []Entry) Tokens {
	var t Tokens
	for _, e := range entries {
		t = t.Add(e.Tokens)
	}
	return t
}

// Since totals every entry at or after cutoff. This is how a rolling
// window is measured: Since(entries, time.Now().Add(-5*time.Hour)) is the
// five-hour window an account's short-term limit is counted over.
func Since(entries []Entry, cutoff time.Time) Tokens {
	var t Tokens
	for _, e := range entries {
		if !e.At.Before(cutoff) {
			t = t.Add(e.Tokens)
		}
	}
	return t
}

// GroupBy totals entries under whatever key returns. Callers group by
// e.Cwd for a per-project breakdown, e.Model for a per-model one, or
// e.SessionID to find the session that spent the most.
func GroupBy(entries []Entry, key func(Entry) string) map[string]Tokens {
	out := make(map[string]Tokens)
	for _, e := range entries {
		out[key(e)] = out[key(e)].Add(e.Tokens)
	}
	return out
}
