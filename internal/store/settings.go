package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// settingNotifyActivate is the bundle id a notification click brings
// forward when the notification has no URL.
// TODO(owner): no setter yet; the default is the user's terminal.
const settingNotifyActivate = "notify_activate_bundle_id"

// NotifyActivateBundleID returns the configured terminal bundle id, or ""
// when unset; the default lives in internal/notify, next to its use.
func (s *Store) NotifyActivateBundleID(ctx context.Context) (string, error) {
	v, err := s.q.GetSetting(ctx, settingNotifyActivate)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading notify activate bundle id: %w", err)
	}
	return v, nil
}
