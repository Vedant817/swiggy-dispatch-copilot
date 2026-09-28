package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Addr            string        `yaml:"addr"`
		ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	} `yaml:"server"`
	Postgres struct {
		DSNEnv string `yaml:"dsn_env"`
	} `yaml:"postgres"`
	Redis struct {
		AddrEnv string `yaml:"addr_env"`
	} `yaml:"redis"`
	Assign struct {
		WorkerCount    int           `yaml:"worker_count"`
		OfferTTL       time.Duration `yaml:"offer_ttl"`
		MaxCandidates  int           `yaml:"max_candidates"`
		LockTTL        time.Duration `yaml:"lock_ttl"`
		ReofferBackoff time.Duration `yaml:"reoffer_backoff"`
	} `yaml:"assign"`
	Scoring struct {
		Weights struct {
			DistanceKm float64 `yaml:"distance_km"`
			RiderLoad  float64 `yaml:"rider_load"`
			Rating     float64 `yaml:"rating"`
			VipBonus   float64 `yaml:"vip_bonus"`
		} `yaml:"weights"`
	} `yaml:"scoring"`
	Generator struct {
		Seed            int64     `yaml:"seed"`
		CityBBox        []float64 `yaml:"city_bbox"`
		Restaurants     int       `yaml:"restaurants"`
		Riders          int       `yaml:"riders"`
		OrderRatePerMin int       `yaml:"order_rate_per_min"`
		VipRatio        float64   `yaml:"vip_ratio"`
		LocationTickMs  int       `yaml:"location_tick_ms"`
	} `yaml:"generator"`
	Agent struct {
		BaseURL      string `yaml:"base_url"`
		ModelEnv     string `yaml:"model_env"`
		MaxToolCalls int    `yaml:"max_tool_calls"`
	} `yaml:"agent"`
	Proposals struct {
		TTL time.Duration `yaml:"ttl"`
	} `yaml:"proposals"`
	Evals struct {
		Seed          int64   `yaml:"seed"`
		ScenarioCount int     `yaml:"scenario_count"`
		MinPassRate   float64 `yaml:"min_pass_rate"`
	} `yaml:"evals"`
}

func DefaultPath() string {
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		return v
	}
	return "../configs/default.yaml"
}

func Load(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	// Env overrides for server addr.
	if v := os.Getenv("PORT"); v != "" {
		c.Server.Addr = ":" + v
	}
	if v := os.Getenv("SHUTDOWN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Server.ShutdownTimeout = d
		}
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// ValidateProductionAuth is called by the public API process. The background
// worker has no HTTP role and does not receive the role secrets.
func (c Config) ValidateProductionAuth() error {
	if strings.EqualFold(c.AppEnv(), "prod") {
		seen := make(map[string]string)
		for _, role := range []string{"SERVICE_TOKEN", "AGENT_TOKEN", "OPS_TOKEN", "WEBHOOK_TOKEN", "ADMIN_TOKEN"} {
			value := os.Getenv(role)
			if len(value) < 24 {
				return fmt.Errorf("%s must have at least 24 characters in prod", role)
			}
			if other, ok := seen[value]; ok {
				return fmt.Errorf("%s and %s must have different values in prod", role, other)
			}
			seen[value] = role
		}
	}
	return nil
}

func (c Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr required")
	}
	if c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("server.shutdown_timeout must be > 0")
	}
	if c.Assign.WorkerCount <= 0 || c.Assign.WorkerCount > 128 {
		return fmt.Errorf("assign.worker_count must be 1..128")
	}
	if c.Assign.OfferTTL <= 0 || c.Assign.LockTTL <= 0 || c.Assign.ReofferBackoff < 0 {
		return fmt.Errorf("assign TTLs must be positive (reoffer_backoff >= 0)")
	}
	if c.Assign.LockTTL >= c.Assign.OfferTTL {
		return fmt.Errorf("assign.lock_ttl must be < offer_ttl")
	}
	if c.Assign.MaxCandidates <= 0 {
		return fmt.Errorf("assign.max_candidates must be > 0")
	}
	if len(c.Generator.CityBBox) != 4 {
		return fmt.Errorf("generator.city_bbox must have 4 elements")
	}
	if c.Generator.Restaurants < 0 || c.Generator.Riders < 0 {
		return fmt.Errorf("generator counts must be >= 0")
	}
	if c.Generator.VipRatio < 0 || c.Generator.VipRatio > 1 {
		return fmt.Errorf("generator.vip_ratio must be 0..1")
	}
	if c.Agent.MaxToolCalls <= 0 {
		return fmt.Errorf("agent.max_tool_calls must be > 0")
	}
	if c.Proposals.TTL <= 0 {
		return fmt.Errorf("proposals.ttl must be > 0")
	}
	if c.Evals.ScenarioCount <= 0 {
		return fmt.Errorf("evals.scenario_count must be > 0")
	}
	if c.Evals.MinPassRate < 0 || c.Evals.MinPassRate > 1 {
		return fmt.Errorf("evals.min_pass_rate must be 0..1")
	}
	return nil
}

func (c Config) PostgresDSN() string {
	return os.Getenv(c.Postgres.DSNEnv)
}

func (c Config) RedisAddr() string {
	return os.Getenv(c.Redis.AddrEnv)
}

func (c Config) AdminToken() string { return os.Getenv("ADMIN_TOKEN") }
func (c Config) OpsToken() string   { return os.Getenv("OPS_TOKEN") }
func (c Config) AppEnv() string {
	if v := os.Getenv("APP_ENV"); v != "" {
		return v
	}
	return "local"
}
