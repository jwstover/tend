package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/usage"
)

// usagePollInterval is how often the transcript scan looks for new
// traffic. A full parse of a real tree (52 transcripts, 34MB) is about a
// tenth of a second, and the fingerprint guard means an unchanged tree
// costs one stat per file instead -- but token totals move over hours.
// There is nothing to see at a shorter cadence, and the header's quota
// segment already carries the number that moves by the minute.
const usagePollInterval = 5 * time.Minute

// usageMsg carries one transcript scan to the event loop. It is sent only
// when the tree has actually moved (see pollUsage), so an idle machine
// produces no messages at all.
type usageMsg struct {
	summary usage.Summary
	entries []usage.Entry
	skipped int
	err     error
}

// pollUsage rescans the transcript tree at root, but only when a
// fingerprint says it has moved since prev -- a nil prev being the first
// tick, which always scans. It reports the new fingerprint and whether
// the message is worth sending, mirroring pollRuns' (prev) -> (next,
// changed) shape (sessions.go).
//
// A fingerprint that cannot be taken falls through to the scan rather
// than freezing the reading: whatever is wrong with the tree then shows
// up as the scan's own error, and the zero fingerprint returned makes the
// next tick try again.
func pollUsage(root string, prev *usage.Fingerprint) (usageMsg, usage.Fingerprint, bool) {
	fp, err := usage.FingerprintTranscripts(root)
	switch {
	case err != nil:
		fp = usage.Fingerprint{}
	case prev != nil && prev.Equal(fp):
		return usageMsg{}, fp, false
	}
	entries, skipped, serr := usage.ScanTranscripts(root)
	if serr != nil {
		return usageMsg{err: serr}, usage.Fingerprint{}, true
	}
	// Rolled up here, on the poller's goroutine, and never in View: this
	// walks every message on the machine.
	return usageMsg{
		summary: usage.Summarize(entries, time.Now()),
		entries: entries,
		skipped: skipped,
	}, fp, true
}

// runUsagePoller scans once straight away, so a view has a reading from
// the first frame rather than five minutes in, then on every tick until
// ctx is canceled. Like runQuotaPoller it is a goroutine rather than a
// tea.Cmd so it keeps running while a session holds the terminal (see Run
// in app.go); the fingerprint it carries between ticks lives here, on the
// goroutine, the way runSessionPoller holds its run snapshot.
func runUsagePoller(ctx context.Context, root string, interval time.Duration, send func(tea.Msg)) {
	var prev *usage.Fingerprint
	poll := func() {
		msg, next, changed := pollUsage(root, prev)
		prev = &next
		if !changed || ctx.Err() != nil {
			return
		}
		send(msg)
	}
	poll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

// startUsagePoller runs runUsagePoller against claude's own transcript
// directory. A home directory that cannot be resolved means there is
// nothing to read, and the app simply carries no usage reading -- the
// same way a missing claude leaves the quota segment empty.
func startUsagePoller(ctx context.Context, send func(tea.Msg)) {
	root, err := usage.TranscriptRoot()
	if err != nil {
		return
	}
	go runUsagePoller(ctx, root, usagePollInterval, send)
}

// applyUsage folds one scan into the app. A failed scan keeps the last
// reading rather than blanking it -- the next tick tries again -- which
// is the bargain applyQuota strikes too.
func (a app) applyUsage(msg usageMsg) app {
	if msg.err != nil {
		return a
	}
	a.usage, a.usageEntries, a.usageSkipped, a.usageLoaded = msg.summary, msg.entries, msg.skipped, true
	return a
}
