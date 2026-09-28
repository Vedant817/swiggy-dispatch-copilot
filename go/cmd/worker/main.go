package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
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
	log.Printf("worker running (expiry poller wired in B4), offer_ttl=%v", cfg.Assign.OfferTTL)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	time.Sleep(100 * time.Millisecond)
}
