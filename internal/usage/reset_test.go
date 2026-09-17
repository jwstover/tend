package usage

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	return loc
}

// TestParseResetTimeWithMinutes and TestParseResetTimeWithoutMinutes build
// their clause from time.Now(), a few hours out, rather than a hard-coded
// date: a fixed "Sep 15" clause is less than 24h old only on and shortly
// after that date, and ParseResetTime rolls anything older into next year
// (see TestParseResetTimeInfersYear), so a fixed fixture goes flaky the
// moment the wall clock crosses that boundary.
func TestParseResetTimeWithMinutes(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	want := time.Now().In(ny).Add(3 * time.Hour).Truncate(time.Minute)
	if want.Minute() == 0 {
		want = want.Add(time.Minute)
	}
	clause := want.Format("Jan 2, 3:04pm") + " (America/New_York)"

	got, ok := ParseResetTime(clause)
	if !ok {
		t.Fatalf("ParseResetTime(%q): want ok, got false", clause)
	}
	if !got.In(ny).Equal(want) {
		t.Errorf("got %v, want %v", got.In(ny), want)
	}
}

func TestParseResetTimeWithoutMinutes(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	now := time.Now().In(ny)
	want := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, ny).Add(3 * time.Hour)
	clause := want.Format("Jan 2, 3pm") + " (America/New_York)"

	got, ok := ParseResetTime(clause)
	if !ok {
		t.Fatalf("ParseResetTime(%q): want ok, got false", clause)
	}
	if !got.In(ny).Equal(want) {
		t.Errorf("got %v, want %v", got.In(ny), want)
	}
}

func TestParseResetTimeNoZoneSuffix(t *testing.T) {
	for _, clause := range []string{"Fri", "3:30pm"} {
		if _, ok := ParseResetTime(clause); ok {
			t.Errorf("ParseResetTime(%q): want not ok", clause)
		}
	}
}

func TestParseResetTimeBadZone(t *testing.T) {
	if _, ok := ParseResetTime("Sep 15, 3:30pm (Nowhere/Fake)"); ok {
		t.Errorf("ParseResetTime with unloadable zone: want not ok")
	}
}

func TestParseResetTimeUnknownLayout(t *testing.T) {
	if _, ok := ParseResetTime("the 15th (America/New_York)"); ok {
		t.Errorf("ParseResetTime with unknown layout: want not ok")
	}
}

func TestParseResetTimeInfersYear(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	base := time.Now().In(ny).Add(-72 * time.Hour)
	clause := base.Format("Jan 2, 3:04pm") + " (America/New_York)"

	got, ok := ParseResetTime(clause)
	if !ok {
		t.Fatalf("ParseResetTime(%q): want ok, got false", clause)
	}
	if got.Before(time.Now().Add(-24 * time.Hour)) {
		t.Errorf("got %v, want no earlier than 24h in the past", got)
	}
	wantDateTime := base.Format("Jan 2, 3:04pm")
	if gotDateTime := got.In(ny).Format("Jan 2, 3:04pm"); gotDateTime != wantDateTime {
		t.Errorf("got date-time %v, want %v", gotDateTime, wantDateTime)
	}
}
