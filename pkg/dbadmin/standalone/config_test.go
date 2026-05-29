package standalone

import (
	"strings"
	"testing"
)

func TestLoadConfig_DefaultsAndValidate(t *testing.T) {
	cfg, err := LoadConfig("testdata/config.full.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Listen != "127.0.0.1:7878" {
		t.Fatalf("unexpected listen: %q", cfg.Listen)
	}
	if cfg.Auth.PasswordMinLength != 14 {
		t.Fatalf("unexpected min length: %d", cfg.Auth.PasswordMinLength)
	}
}

func TestLoadConfig_RejectsInlineSecrets(t *testing.T) {
	_, err := LoadConfig("testdata/config.bad-secret-inline.yaml")
	if err == nil {
		t.Fatal("expected rejection for inlined secrets")
	}
	if !strings.Contains(err.Error(), "must not be inlined") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_RejectsBadEscalation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RateLimits.LockoutEscalationMinutes = []int{30, 10}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation failure for non-increasing escalation")
	}
}

func TestValidate_RejectsShortPassword(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.PasswordMinLength = 8
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation failure for min < 14")
	}
}

func TestToDBAdminConfig_Mirrors(t *testing.T) {
	cfg := DefaultConfig()
	d := cfg.ToDBAdminConfig()
	if d.Session.IdleTTL != cfg.Session.IdleTTL {
		t.Fatal("session.idle_ttl not mirrored")
	}
	if d.Query.PoolSizePerConn != cfg.Query.PoolSizePerConn {
		t.Fatal("query.pool_size not mirrored")
	}
}
