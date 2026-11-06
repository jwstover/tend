package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jwstover/tend/internal/store/gen"
)

// settingNotifyActivate is the bundle id a notification click brings
// forward when the notification has no URL.
const settingNotifyActivate = "notify_activate_bundle_id"

// Setting returns the stored value for key; ok is false when it is unset.
// Validation belongs to internal/settings, not here.
func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	v, err := s.q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading setting %q: %w", key, err)
	}
	return v, true, nil
}

// SetSetting stores value under key, replacing any previous value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if err := s.q.SetSetting(ctx, gen.SetSettingParams{Key: key, Value: value}); err != nil {
		return fmt.Errorf("writing setting %q: %w", key, err)
	}
	return nil
}

// ClearSetting makes key unset again. Clearing an unset key is not an error.
func (s *Store) ClearSetting(ctx context.Context, key string) error {
	if err := s.q.DeleteSetting(ctx, key); err != nil {
		return fmt.Errorf("clearing setting %q: %w", key, err)
	}
	return nil
}

// NotifyActivateBundleID returns the configured terminal bundle id, or ""
// when unset; the default lives in internal/notify, next to its use.
func (s *Store) NotifyActivateBundleID(ctx context.Context) (string, error) {
	v, _, err := s.Setting(ctx, settingNotifyActivate)
	return v, err
}
