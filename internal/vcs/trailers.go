package vcs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TrailerKey is the commit trailer that ties a commit to a tend task.
const TrailerKey = "Tend-Task"

// Commit is a commit that carries a Tend-Task trailer.
type Commit struct {
	SHA     string
	Subject string
	When    time.Time
}

// CommitDiff is one commit's own changes. Tagged commits need not be
// contiguous, so each is shown on its own rather than as a range that would
// pull in untagged work between them.
type CommitDiff struct {
	Commit Commit
	Diff   Diff
}

// taskLogFormat prints, per commit: sha NUL unix-time NUL subject NUL the
// Tend-Task trailer values joined by commas, then a record separator.
const taskLogFormat = "--format=%H%x00%ct%x00%s%x00%(trailers:key=" + TrailerKey + ",valueonly,separator=%x2C)%x1e"

// TaskCommits lists the commits in repo (any ref) whose Tend-Task trailer
// names taskID, oldest first. A trailer may list several ids ("12, 790").
func (c *Client) TaskCommits(ctx context.Context, repo string, taskID int64) ([]Commit, error) {
	out, err := c.git(ctx, repo, "log", "--all", "--topo-order", "--reverse", taskLogFormat)
	if err != nil {
		return nil, err
	}
	return parseTaskCommits(string(out), taskID), nil
}

// parseTaskCommits filters `git log` output in taskLogFormat to taskID.
// Records it cannot read are skipped, not fatal.
func parseTaskCommits(out string, taskID int64) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\r\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, "\x00")
		if len(f) != 4 || !trailerNames(f[3], taskID) {
			continue
		}
		var when time.Time
		if ts, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			when = time.Unix(ts, 0).UTC()
		}
		commits = append(commits, Commit{SHA: f[0], Subject: f[2], When: when})
	}
	return commits
}

// trailerNames reports whether the comma-separated trailer values contain
// exactly id (so 790 does not match 7900).
func trailerNames(values string, id int64) bool {
	for _, v := range strings.Split(values, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n == id {
			return true
		}
	}
	return false
}

// TaskCommitDiffs is TaskCommits plus each commit's own diff, oldest first.
// A merge commit is diffed against its first parent.
func (c *Client) TaskCommitDiffs(ctx context.Context, repo string, taskID int64) ([]CommitDiff, error) {
	commits, err := c.TaskCommits(ctx, repo, taskID)
	if err != nil {
		return nil, err
	}
	var res []CommitDiff
	for _, cm := range commits {
		out, err := c.git(ctx, repo, "show", "--format=", "--no-color", "--no-ext-diff", "-U3", "-M", "--first-parent", cm.SHA)
		if err != nil {
			return nil, fmt.Errorf("showing %s: %w", cm.SHA, err)
		}
		d, err := ParseUnified(string(out))
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", cm.SHA, err)
		}
		res = append(res, CommitDiff{Commit: cm, Diff: d})
	}
	return res, nil
}
