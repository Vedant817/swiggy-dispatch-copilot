package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/api"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/redisx"
	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/store"
)

func resolveConfig() string {
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		return v
	}
	for _, p := range []string{"./configs/default.yaml", "../configs/default.yaml", "../../configs/default.yaml", config.DefaultPath()} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return config.DefaultPath()
}

func main() {
	cfgPath := resolveConfig()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.ValidateProductionAuth(); err != nil {
		log.Fatalf("production auth: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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

	srv := api.New(cfg, st, rdb)
	workerCtx, workerStop := context.WithCancel(context.Background())
	defer workerStop()
	go srv.Assign.Start(workerCtx)
	httpSrv := &http.Server{Addr: cfg.Server.Addr, Handler: srv}
	go func() {
		log.Printf("api listening on %s", cfg.Server.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	workerStop()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
}
