// Package jira turns pasted Jira issue URLs into expanded task titles.
// It owns the two I/O edges this requires: the Jira REST API (one GET
// per import) and the system keychain (credential storage). Capture
// must never block on it: every failure degrades to the bare issue key.
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// fetchTimeout bounds the summary lookup so `tend add` stays snappy
// even when Jira is slow or unreachable.
const fetchTimeout = 5 * time.Second

// Issue is a Jira issue reference parsed from a pasted URL.
type Issue struct {
	// BaseURL is the instance root the REST API lives under, e.g.
	// "https://example.atlassian.net" (plus any context path on
	// self-hosted instances).
	BaseURL string
	Key     string // e.g. "PROJ-123"
	URL     string // the original pasted URL, preserved for the task body
}

var keyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)

// ParseIssueURL recognizes the two shapes Jira issue links come in: a
// browse URL (…/browse/KEY) and a board/backlog URL carrying a
// selectedIssue=KEY query parameter.
func ParseIssueURL(raw string) (Issue, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Issue{}, false
	}

	if i := strings.Index(u.Path, "/browse/"); i >= 0 {
		key := strings.Trim(u.Path[i+len("/browse/"):], "/")
		if keyRe.MatchString(key) {
			base := *u
			base.Path = u.Path[:i]
			base.RawQuery, base.Fragment = "", ""
			return Issue{BaseURL: base.String(), Key: key, URL: raw}, true
		}
	}

	// Board/backlog URLs put the issue in the query string. Their paths
	// are UI routes, not API roots, so the instance root is the bare
	// host (true for Jira Cloud; self-hosted boards should paste the
	// browse URL instead).
	if key := u.Query().Get("selectedIssue"); keyRe.MatchString(key) {
		base := url.URL{Scheme: u.Scheme, Host: u.Host}
		return Issue{BaseURL: base.String(), Key: key, URL: raw}, true
	}

	return Issue{}, false
}

// FetchSummary asks the Jira REST API for the issue's summary line.
func FetchSummary(ctx context.Context, iss Issue, creds Credentials) (string, error) {
	endpoint := fmt.Sprintf("%s/rest/api/2/issue/%s?fields=summary", iss.BaseURL, iss.Key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.SetBasicAuth(creds.Email, creds.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", iss.Key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: %s", iss.Key, resp.Status)
	}

	var payload struct {
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decoding %s response: %w", iss.Key, err)
	}
	if payload.Fields.Summary == "" {
		return "", fmt.Errorf("issue %s has no summary", iss.Key)
	}
	return payload.Fields.Summary, nil
}

// Expand resolves a parsed issue into a task title. It never fails:
// without stored credentials, on a site mismatch, or on any fetch error
// it falls back to the bare issue key and returns the reason as a
// non-nil warn so the caller can surface it without blocking capture.
func Expand(ctx context.Context, iss Issue) (title string, warn error) {
	creds, err := LoadCredentials()
	if err != nil {
		return iss.Key, err
	}
	if !creds.Matches(iss) {
		return iss.Key, fmt.Errorf("%s is not on the authenticated site %s", iss.URL, creds.Site)
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	summary, err := FetchSummary(ctx, iss, creds)
	if err != nil {
		return iss.Key, err
	}
	return iss.Key + ": " + summary, nil
}
