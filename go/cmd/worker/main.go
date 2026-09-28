package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/assign"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/obs"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/redisx"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
)

func main() {
	cfgPath := os.Getenv("CONFIG_PATH")
	if cfgPath == "" {
		cfgPath = "../configs/default.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, cfg.PostgresDSN())
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	rdb, err := redisx.Dial(cfg.RedisAddr())
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rdb.Close()
	svc := assign.New(cfg, st, rdb, obs.NewCounters())
	log.Printf("worker running, offer_ttl=%v workers=%d", cfg.Assign.OfferTTL, cfg.Assign.WorkerCount)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	svc.Start(ctx)
}
