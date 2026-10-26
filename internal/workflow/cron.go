package workflow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidCron wraps every cron parse failure, and Next's give-up when no
// minute within five years matches (e.g. `0 0 30 2 *`).
var ErrInvalidCron = errors.New("invalid cron expression")

// Cron is a parsed five-field cron expression (minute hour day-of-month
// month day-of-week). The zero value matches nothing; build one with ParseCron.
type Cron struct {
	min, hour, dom, month, dow uint64
	domStar, dowStar           bool
	expr                       string
}

type cronField struct {
	name     string
	min, max int
	names    []string // names[i] is the name of value min+i, when the field has any
}

var (
	cronMinute = cronField{name: "minute", min: 0, max: 59}
	cronHour   = cronField{name: "hour", min: 0, max: 23}
	cronDom    = cronField{name: "day-of-month", min: 1, max: 31}
	cronMonth  = cronField{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}}
	cronDow    = cronField{name: "day-of-week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}}
)

var cronMacros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
}

// ParseCron parses a five-field cron expression or one of the @hourly,
// @daily (@midnight), @weekly and @monthly macros.
func ParseCron(expr string) (Cron, error) {
	s := strings.TrimSpace(expr)
	if m, ok := cronMacros[strings.ToLower(s)]; ok {
		s = m
	}
	parts := strings.Fields(s)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("%w: want 5 fields (minute hour day-of-month month day-of-week), got %d", ErrInvalidCron, len(parts))
	}
	c := Cron{expr: strings.Join(parts, " ")}
	var err error
	if c.min, err = parseCronField(parts[0], cronMinute); err != nil {
		return Cron{}, err
	}
	if c.hour, err = parseCronField(parts[1], cronHour); err != nil {
		return Cron{}, err
	}
	if c.dom, err = parseCronField(parts[2], cronDom); err != nil {
		return Cron{}, err
	}
	if c.month, err = parseCronField(parts[3], cronMonth); err != nil {
		return Cron{}, err
	}
	if c.dow, err = parseCronField(parts[4], cronDow); err != nil {
		return Cron{}, err
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow = c.dow&^(1<<7) | 1
	}
	c.domStar = strings.HasPrefix(parts[2], "*")
	c.dowStar = strings.HasPrefix(parts[4], "*")
	return c, nil
}

func parseCronField(s string, f cronField) (uint64, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s %q %s", ErrInvalidCron, f.name, s, fmt.Sprintf(format, a...))
	}
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		base, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return 0, bad("has an invalid step")
			}
			step = n
		}
		lo, hi := f.min, f.max
		if base != "*" {
			a, b, isRange := strings.Cut(base, "-")
			var err error
			if lo, err = cronValue(a, f); err != nil {
				return 0, bad("%s", err)
			}
			switch {
			case isRange:
				if hi, err = cronValue(b, f); err != nil {
					return 0, bad("%s", err)
				}
			case !hasStep:
				hi = lo
			}
			if lo > hi {
				return 0, bad("has a reversed range")
			}
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func cronValue(s string, f cronField) (int, error) {
	for i, n := range f.names {
		if strings.EqualFold(s, n) {
			return f.min + i, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.New("is not a number or name")
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("out of range %d-%d", f.min, f.max)
	}
	return v, nil
}

// String is the normalized expression: macros expanded, fields single-spaced.
func (c Cron) String() string { return c.expr }

func (c Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	if !c.domStar && !c.dowStar { // Vixie: either one, when both are restricted
		return dom || dow
	}
	return dom && dow
}

// Next returns the first minute strictly after `after` that matches, in
// after's location, with zero seconds. It gives up after five years.
func (c Cron) Next(after time.Time) (time.Time, error) {
	loc := after.Location()
	limit := after.AddDate(5, 0, 0)
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, loc).Add(time.Minute)
	for t.Before(limit) {
		switch {
		case c.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case c.hour&(1<<uint(t.Hour())) == 0:
			// Add, not Date: stepping absolute time cannot loop in a repeated DST hour.
			t = t.Add(time.Duration(60-t.Minute()) * time.Minute)
		case c.min&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			// A repeated local time (fall back) fires once, on its first pass.
			if p := t.Add(-time.Hour); p.Hour() == t.Hour() && p.Minute() == t.Minute() && p.Day() == t.Day() {
				t = t.Add(time.Minute)
				continue
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: %q has no run within five years of %s", ErrInvalidCron, c.expr, after.Format(time.RFC3339))
}
