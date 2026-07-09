package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestParseIssueURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Issue
		ok   bool
	}{
		{
			name: "cloud browse URL",
			raw:  "https://example.atlassian.net/browse/PROJ-123",
			want: Issue{
				BaseURL: "https://example.atlassian.net",
				Key:     "PROJ-123",
				URL:     "https://example.atlassian.net/browse/PROJ-123",
			},
			ok: true,
		},
		{
			name: "browse URL with query and fragment",
			raw:  "https://example.atlassian.net/browse/PROJ-123?focusedCommentId=42#comment-42",
			want: Issue{
				BaseURL: "https://example.atlassian.net",
				Key:     "PROJ-123",
				URL:     "https://example.atlassian.net/browse/PROJ-123?focusedCommentId=42#comment-42",
			},
			ok: true,
		},
		{
			name: "self-hosted with context path",
			raw:  "https://jira.example.com/jira/browse/DEV-7",
			want: Issue{
				BaseURL: "https://jira.example.com/jira",
				Key:     "DEV-7",
				URL:     "https://jira.example.com/jira/browse/DEV-7",
			},
			ok: true,
		},
		{
			name: "board URL with selectedIssue",
			raw:  "https://example.atlassian.net/jira/software/c/projects/PROJ/boards/1?selectedIssue=PROJ-9",
			want: Issue{
				BaseURL: "https://example.atlassian.net",
				Key:     "PROJ-9",
				URL:     "https://example.atlassian.net/jira/software/c/projects/PROJ/boards/1?selectedIssue=PROJ-9",
			},
			ok: true,
		},
		{name: "plain title", raw: "buy milk", ok: false},
		{name: "non-jira URL", raw: "https://example.com/docs/readme", ok: false},
		{name: "browse of a lowercase key", raw: "https://example.atlassian.net/browse/proj-1", ok: false},
		{name: "browse of a non-key page", raw: "https://example.atlassian.net/browse/PROJ-123/extra", ok: false},
		{name: "missing host", raw: "/browse/PROJ-123", ok: false},
		{name: "non-http scheme", raw: "ftp://example.com/browse/PROJ-1", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseIssueURL(tt.raw)
			if ok != tt.ok {
				t.Fatalf("ParseIssueURL(%q) ok = %v, want %v", tt.raw, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("ParseIssueURL(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

// summaryServer serves the one endpoint FetchSummary hits, asserting
// basic auth arrives intact.
func summaryServer(t *testing.T, key, summary string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issue/"+key {
			http.NotFound(w, r)
			return
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "me@example.com" || pass != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"fields":{"summary":%q}}`, summary)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchSummary(t *testing.T) {
	srv := summaryServer(t, "PROJ-1", "Fix the flux capacitor")
	creds := Credentials{Site: srv.URL, Email: "me@example.com", Token: "tok"}

	got, err := FetchSummary(context.Background(), Issue{BaseURL: srv.URL, Key: "PROJ-1"}, creds)
	if err != nil {
		t.Fatalf("FetchSummary: %v", err)
	}
	if got != "Fix the flux capacitor" {
		t.Errorf("summary = %q", got)
	}

	if _, err := FetchSummary(context.Background(), Issue{BaseURL: srv.URL, Key: "PROJ-404"}, creds); err == nil {
		t.Error("expected an error for an unknown issue")
	}

	bad := Credentials{Site: srv.URL, Email: "me@example.com", Token: "wrong"}
	if _, err := FetchSummary(context.Background(), Issue{BaseURL: srv.URL, Key: "PROJ-1"}, bad); err == nil {
		t.Error("expected an error for bad credentials")
	}
}

func TestExpandWithoutCredentials(t *testing.T) {
	keyring.MockInit()

	iss := Issue{BaseURL: "https://example.atlassian.net", Key: "PROJ-5", URL: "https://example.atlassian.net/browse/PROJ-5"}
	title, warn := Expand(context.Background(), iss)
	if title != "PROJ-5" {
		t.Errorf("title = %q, want bare key", title)
	}
	if !errors.Is(warn, ErrNoCredentials) {
		t.Errorf("warn = %v, want ErrNoCredentials", warn)
	}
}

func TestExpandFetchesTitle(t *testing.T) {
	keyring.MockInit()
	srv := summaryServer(t, "PROJ-1", "Fix the flux capacitor")

	creds := Credentials{Site: srv.URL, Email: "me@example.com", Token: "tok"}
	if err := SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	iss := Issue{BaseURL: srv.URL, Key: "PROJ-1", URL: srv.URL + "/browse/PROJ-1"}
	title, warn := Expand(context.Background(), iss)
	if warn != nil {
		t.Fatalf("Expand warn = %v", warn)
	}
	if title != "PROJ-1: Fix the flux capacitor" {
		t.Errorf("title = %q", title)
	}
}

func TestExpandRefusesForeignHost(t *testing.T) {
	keyring.MockInit()

	creds := Credentials{Site: "https://example.atlassian.net", Email: "me@example.com", Token: "tok"}
	if err := SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	iss := Issue{BaseURL: "https://evil.example.com", Key: "PROJ-1", URL: "https://evil.example.com/browse/PROJ-1"}
	title, warn := Expand(context.Background(), iss)
	if title != "PROJ-1" {
		t.Errorf("title = %q, want bare key", title)
	}
	if warn == nil || !strings.Contains(warn.Error(), "not on the authenticated site") {
		t.Errorf("warn = %v, want a site-mismatch warning", warn)
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	keyring.MockInit()

	want := Credentials{Site: "https://example.atlassian.net", Email: "me@example.com", Token: "tok"}
	if err := SaveCredentials(want); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	got, err := LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if got != want {
		t.Errorf("LoadCredentials = %+v, want %+v", got, want)
	}

	if err := DeleteCredentials(); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if _, err := LoadCredentials(); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("after delete, err = %v, want ErrNoCredentials", err)
	}
	if err := DeleteCredentials(); err != nil {
		t.Errorf("second DeleteCredentials = %v, want nil", err)
	}
}
