package store

import (
	"context"
	"testing"

	"github.com/jwstover/tend/internal/store/gen"
)

func TestNotifyActivateBundleID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if got, err := s.NotifyActivateBundleID(ctx); err != nil || got != "" {
		t.Fatalf("unset = %q, %v; want empty", got, err)
	}
	if err := s.q.SetSetting(ctx, gen.SetSettingParams{Key: settingNotifyActivate, Value: "com.apple.Terminal"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.NotifyActivateBundleID(ctx); err != nil || got != "com.apple.Terminal" {
		t.Errorf("set = %q, %v", got, err)
	}
}
