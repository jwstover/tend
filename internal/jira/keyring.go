package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "tend"
	keyringUser    = "jira"
)

// ErrNoCredentials is returned when nothing is stored in the keychain
// yet; callers surface it as a hint to run `tend auth login`.
var ErrNoCredentials = errors.New("no Jira credentials stored (run `tend auth jira login`)")

// Credentials are the Jira basic-auth credentials, stored as a single
// JSON blob in the system keychain.
type Credentials struct {
	Site  string `json:"site"` // instance root, e.g. https://example.atlassian.net
	Email string `json:"email"`
	Token string `json:"token"`
}

// Matches reports whether the issue lives on the authenticated site, so
// the token is only ever sent to the host it was stored for.
func (c Credentials) Matches(iss Issue) bool {
	site, err := url.Parse(c.Site)
	if err != nil {
		return false
	}
	base, err := url.Parse(iss.BaseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(site.Host, base.Host)
}

// LoadCredentials reads the stored credentials from the system keychain.
func LoadCredentials() (Credentials, error) {
	blob, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credentials{}, ErrNoCredentials
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("reading keychain: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal([]byte(blob), &c); err != nil {
		return Credentials{}, fmt.Errorf("decoding stored credentials: %w", err)
	}
	return c, nil
}

// SaveCredentials writes the credentials to the system keychain,
// replacing any previous entry.
func SaveCredentials(c Credentials) error {
	blob, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}
	if err := keyring.Set(keyringService, keyringUser, string(blob)); err != nil {
		return fmt.Errorf("writing keychain: %w", err)
	}
	return nil
}

// DeleteCredentials removes the stored credentials. Deleting when
// nothing is stored is not an error.
func DeleteCredentials() error {
	err := keyring.Delete(keyringService, keyringUser)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("clearing keychain: %w", err)
	}
	return nil
}
