package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/workflow"
)

type tickStore struct {
	due       []workflow.Schedule
	dueErr    error
	claims    map[int64]workflow.ScheduleClaim
	claimErrs map[int64]error
	claimed   []int64
	failed    map[int64]string
	gotNow    time.Time
}

func (f *tickStore) DueSchedules(_ context.Context, now time.Time) ([]workflow.Schedule, error) {
	f.gotNow = now
	return f.due, f.dueErr
}

func (f *tickStore) ClaimSchedule(_ context.Context, id int64, _ time.Time) (workflow.ScheduleClaim, error) {
	f.claimed = append(f.claimed, id)
	if err := f.claimErrs[id]; err != nil {
		return workflow.ScheduleClaim{}, err
	}
	return f.claims[id], nil
}

func (f *tickStore) FailRun(_ context.Context, id int64, reason string) error {
	if f.failed == nil {
		f.failed = map[int64]string{}
	}
	f.failed[id] = reason
	return nil
}

func tickClaim(runID int64) workflow.ScheduleClaim {
	return workflow.ScheduleClaim{Claimed: true, Run: &workflow.Run{ID: runID}}
}

func TestTick(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	two := []workflow.Schedule{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	launchOK := func(_ context.Context, id int64) (string, error) { return "tend-wf-x", nil }
	launchFailFirst := func(_ context.Context, id int64) (string, error) {
		if id == 10 {
			return "", errors.New("no tmux")
		}
		return "tend-wf-x", nil
	}

	tests := []struct {
		name       string
		store      *tickStore
		launch     LaunchFunc
		guard      TickGuard
		started    int
		skipped    int
		failed     int
		claimedIDs []int64
		failedRuns map[int64]string
	}{
		{
			name:       "two due start both",
			store:      &tickStore{due: two, claims: map[int64]workflow.ScheduleClaim{1: tickClaim(10), 2: tickClaim(20)}},
			launch:     launchOK,
			started:    2,
			claimedIDs: []int64{1, 2},
		},
		{
			name:       "launch error fails run, other still starts",
			store:      &tickStore{due: two, claims: map[int64]workflow.ScheduleClaim{1: tickClaim(10), 2: tickClaim(20)}},
			launch:     launchFailFirst,
			started:    1,
			failed:     1,
			claimedIDs: []int64{1, 2},
			failedRuns: map[int64]string{10: "no tmux"},
		},
		{
			name: "skipped and lost claims",
			store: &tickStore{due: two, claims: map[int64]workflow.ScheduleClaim{
				1: {Claimed: true, Skipped: "last run still live"},
			}},
			launch:     launchOK,
			skipped:    1,
			claimedIDs: []int64{1, 2},
		},
		{
			name:       "claim error",
			store:      &tickStore{due: two[:1], claimErrs: map[int64]error{1: errors.New("db")}},
			launch:     launchOK,
			failed:     1,
			claimedIDs: []int64{1},
		},
		{
			name:    "guard skips all unclaimed",
			store:   &tickStore{due: two},
			launch:  launchOK,
			guard:   func(context.Context) string { return "quota" },
			skipped: 2,
		},
		{
			name:    "no due schedules never asks the guard",
			store:   &tickStore{},
			launch:  launchOK,
			guard:   func(context.Context) string { t.Error("guard called"); return "x" },
			started: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Tick(context.Background(), tt.store, now, tt.launch, tt.guard)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Started) != tt.started || len(res.Skipped) != tt.skipped || len(res.Failed) != tt.failed {
				t.Fatalf("got started=%d skipped=%d failed=%d, want %d/%d/%d", len(res.Started), len(res.Skipped), len(res.Failed), tt.started, tt.skipped, tt.failed)
			}
			if len(tt.store.claimed) != len(tt.claimedIDs) {
				t.Errorf("claimed %v, want %v", tt.store.claimed, tt.claimedIDs)
			}
			for id, reason := range tt.failedRuns {
				if tt.store.failed[id] != reason {
					t.Errorf("FailRun(%d) = %q, want %q", id, tt.store.failed[id], reason)
				}
			}
			for _, f := range res.Failed {
				if f.Err == nil {
					t.Error("failed entry without error")
				}
			}
			for _, s := range res.Started {
				if s.TmuxSession != "tend-wf-x" || s.RunID == 0 {
					t.Errorf("bad started entry %+v", s)
				}
			}
			if tt.name != "no due schedules never asks the guard" && !tt.store.gotNow.Equal(now) {
				t.Errorf("now = %v", tt.store.gotNow)
			}
		})
	}
}

func TestTickDueError(t *testing.T) {
	_, err := Tick(context.Background(), &tickStore{dueErr: errors.New("boom")}, time.Now(), nil, nil)
	if err == nil {
		t.Fatal("want error")
	}
}

func stubQuota(t *testing.T, q agent.Quota, err error) *int {
	t.Helper()
	calls := 0
	old := fetchQuota
	fetchQuota = func(context.Context) (agent.Quota, error) { calls++; return q, err }
	t.Cleanup(func() { fetchQuota = old })
	return &calls
}

func TestQuotaGuardTick(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	launch := func(context.Context, int64) (string, error) { return "tend-wf-x", nil }
	newStore := func() *tickStore {
		return &tickStore{
			due:    []workflow.Schedule{{ID: 1}, {ID: 2}},
			claims: map[int64]workflow.ScheduleClaim{1: tickClaim(10), 2: tickClaim(20)},
		}
	}
	session := func(p int, resets string) agent.Quota {
		return agent.Quota{Session: &agent.QuotaLimit{Percent: p, Resets: resets}}
	}
	tests := []struct {
		name       string
		quota      agent.Quota
		err        error
		started    int
		skipped    int
		wantReason string
	}{
		{name: "85 percent skips", quota: session(85, "3pm"), skipped: 2, wantReason: "5h quota at 85% (resets 3pm)"},
		{name: "80 percent skips, no resets", quota: session(80, ""), skipped: 2, wantReason: "5h quota at 80%"},
		{name: "79 percent starts", quota: session(79, "3pm"), started: 2},
		{name: "error proceeds", err: errors.New("no claude"), started: 2},
		{name: "nil session proceeds", quota: agent.Quota{}, started: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubQuota(t, tt.quota, tt.err)
			st := newStore()
			res, err := Tick(context.Background(), st, now, launch, QuotaGuard)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Started) != tt.started || len(res.Skipped) != tt.skipped {
				t.Fatalf("started=%d skipped=%d, want %d/%d", len(res.Started), len(res.Skipped), tt.started, tt.skipped)
			}
			if tt.skipped > 0 && len(st.claimed) != 0 {
				t.Errorf("claimed %v while skipped", st.claimed)
			}
			for _, sk := range res.Skipped {
				if sk.Reason != tt.wantReason {
					t.Errorf("reason = %q, want %q", sk.Reason, tt.wantReason)
				}
			}
		})
	}
}

func TestQuotaGuardNothingDueSkipsFetch(t *testing.T) {
	calls := stubQuota(t, agent.Quota{}, nil)
	if _, err := Tick(context.Background(), &tickStore{}, time.Now(), nil, QuotaGuard); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Errorf("fetchQuota called %d times with nothing due", *calls)
	}
}
