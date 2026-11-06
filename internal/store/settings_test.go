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

func TestSettingGetSetClear(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if v, ok, err := s.Setting(ctx, "diff_tool"); err != nil || ok || v != "" {
		t.Fatalf("unset = %q, %v, %v", v, ok, err)
	}
	if err := s.SetSetting(ctx, "diff_tool", "delta"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.Setting(ctx, "diff_tool"); err != nil || !ok || v != "delta" {
		t.Fatalf("set = %q, %v, %v", v, ok, err)
	}
	if err := s.SetSetting(ctx, "diff_tool", "tig"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Setting(ctx, "diff_tool"); v != "tig" {
		t.Errorf("overwrite = %q", v)
	}
	if err := s.ClearSetting(ctx, "diff_tool"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Setting(ctx, "diff_tool"); err != nil || ok {
		t.Errorf("after clear ok=%v err=%v", ok, err)
	}
	if err := s.ClearSetting(ctx, "diff_tool"); err != nil {
		t.Errorf("clearing unset: %v", err)
	}
}
