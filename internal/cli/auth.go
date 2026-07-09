package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jwstover/tend/internal/jira"
)

func newAuthCmd() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage credentials for external services",
	}
	jiraCmd := &cobra.Command{
		Use:   "jira",
		Short: "Manage Jira credentials (stored in the system keychain)",
	}
	jiraCmd.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd())
	auth.AddCommand(jiraCmd)
	return auth
}

func newAuthLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Store Jira site, email, and API token in the system keychain",
		Long: "Prompts for your Jira site URL, account email, and API token " +
			"(create one at https://id.atlassian.com/manage-profile/security/api-tokens) " +
			"and stores them in the system keychain. `tend add <jira-url>` uses " +
			"them to fetch ticket titles.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			in := bufio.NewReader(cmd.InOrStdin())
			out := cmd.OutOrStdout()

			site, err := promptLine(in, out, "Jira site URL (e.g. https://example.atlassian.net): ")
			if err != nil {
				return err
			}
			site, err = normalizeSite(site)
			if err != nil {
				return err
			}

			email, err := promptLine(in, out, "Email: ")
			if err != nil {
				return err
			}
			if email == "" {
				return errors.New("email is required")
			}

			token, err := promptSecret(cmd, in, "API token: ")
			if err != nil {
				return err
			}
			if token == "" {
				return errors.New("API token is required")
			}

			creds := jira.Credentials{Site: site, Email: email, Token: token}
			if err := jira.SaveCredentials(creds); err != nil {
				return err
			}
			fmt.Fprintf(out, "stored credentials for %s (%s) in the system keychain\n", site, email)
			return nil
		},
	}
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which Jira site and account are authenticated",
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := jira.LoadCredentials()
			if errors.Is(err, jira.ErrNoCredentials) {
				fmt.Fprintln(cmd.OutOrStdout(), "not logged in (run `tend auth jira login`)")
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "logged in to %s as %s\n", creds.Site, creds.Email)
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove Jira credentials from the system keychain",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := jira.DeleteCredentials(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "credentials removed")
			return nil
		},
	}
}

// normalizeSite validates the site URL and strips it to its root.
func normalizeSite(s string) (string, error) {
	u, err := url.Parse(strings.TrimRight(s, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid site URL %q (want e.g. https://example.atlassian.net)", s)
	}
	return u.String(), nil
}

func promptLine(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptSecret reads the token without echoing when stdin is a real
// terminal, and falls back to a plain line read otherwise (pipes, tests).
func promptSecret(cmd *cobra.Command, in *bufio.Reader, prompt string) (string, error) {
	out := cmd.OutOrStdout()
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(out, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("reading token: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return promptLine(in, out, prompt)
}
