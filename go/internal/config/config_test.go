package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefault(t *testing.T) {
	p := filepath.Join("..", "..", "..", "configs", "default.yaml")
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Server.Addr != ":8080" {
		t.Fatalf("addr = %q", c.Server.Addr)
	}
	if c.Assign.WorkerCount != 8 {
		t.Fatalf("worker_count = %d", c.Assign.WorkerCount)
	}
	if c.Assign.OfferTTL != 15*time.Second {
		t.Fatalf("offer_ttl = %v", c.Assign.OfferTTL)
	}
}

func TestValidateRejects(t *testing.T) {
	p := filepath.Join("..", "..", "..", "configs", "default.yaml")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Assign.LockTTL = c.Assign.OfferTTL
	if err := c.Validate(); err == nil {
		t.Fatal("expected lock_ttl >= offer_ttl to fail")
	}
	c.Assign.LockTTL = 5 * time.Second
	c.Generator.VipRatio = 2
	if err := c.Validate(); err == nil {
		t.Fatal("expected vip_ratio to fail")
	}
	_ = os.Getenv("PORT")
}

func TestProductionRequiresDistinctRoleSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	p := filepath.Join("..", "..", "..", "configs", "default.yaml")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateProductionAuth(); err == nil {
		t.Fatal("production boot must refuse missing secrets")
	}
	for _, key := range []string{"SERVICE_TOKEN", "AGENT_TOKEN", "OPS_TOKEN", "WEBHOOK_TOKEN", "ADMIN_TOKEN"} {
		t.Setenv(key, "same-value-for-every-role-1234")
	}
	if err := c.ValidateProductionAuth(); err == nil {
		t.Fatal("production boot must refuse reused secrets")
	}
	for i, key := range []string{"SERVICE_TOKEN", "AGENT_TOKEN", "OPS_TOKEN", "WEBHOOK_TOKEN", "ADMIN_TOKEN"} {
		t.Setenv(key, string(rune('a'+i))+"-unique-long-token-for-production")
	}
	if err := c.ValidateProductionAuth(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}
