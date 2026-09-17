package usage

import (
	"regexp"
	"time"
)

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

// ParseResetTime parses a reset clause into a local, tz-aware time. It
// reports false rather than guess when the clause has no zone suffix, the
// zone does not load, or the date-time does not match a layout seen
// before -- callers keep claude's own text and show that instead, the way
// agent.QuotaLimit.Resets does.
//
// The clause carries no year, so one is inferred: this year, unless that
// falls more than a day in the past, in which case the reset is read as
// next year -- the only way a past-dated reset clause makes sense from a
// service that only ever reports limits resetting soon.
func ParseResetTime(clause string) (time.Time, bool) {
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
