package speedlimiter

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/dynamicconfig"
)

func newTestSpeedService(t *testing.T) *Service {
	t.Helper()
	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "speed", option.SpeedLimiterServiceOptions{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	s, ok := rawService.(*Service)
	if !ok {
		t.Fatalf("unexpected service type: %T", rawService)
	}
	return s
}

func TestSpeedServiceApplyDynamicUpdatesManager(t *testing.T) {
	s := newTestSpeedService(t)

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", UploadMbps: 10, DownloadMbps: 20}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}

	config, found := s.GetConfig("bob")
	if !found {
		t.Fatal("expected GetConfig to return config for bob after applyDynamic")
	}
	if config.UploadMbps != 10 {
		t.Errorf("expected UploadMbps=10, got %d", config.UploadMbps)
	}
	if config.DownloadMbps != 20 {
		t.Errorf("expected DownloadMbps=20, got %d", config.DownloadMbps)
	}
}

func TestSpeedServiceRemoveDynamicRemovesFromManager(t *testing.T) {
	s := newTestSpeedService(t)

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", UploadMbps: 10, DownloadMbps: 20}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}
	if _, found := s.GetConfig("bob"); !found {
		t.Fatal("expected bob to have config before remove")
	}

	if err := s.removeDynamic("bob"); err != nil {
		t.Fatalf("removeDynamic: %v", err)
	}

	if _, found := s.GetConfig("bob"); found {
		t.Fatal("expected bob config to be removed after removeDynamic")
	}
}
