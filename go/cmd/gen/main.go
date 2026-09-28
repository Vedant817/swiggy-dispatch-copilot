package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Vedant817/swiggy-dispatch-copilot/go/internal/config"
)

func main() {
	var cfgPath, base string
	var duration time.Duration
	var count, concurrency int
	var seed int64
	var restaurants, riders int
	flag.StringVar(&cfgPath, "config", "../configs/default.yaml", "config path")
	flag.StringVar(&base, "base", "", "API base URL (overrides config)")
	flag.DurationVar(&duration, "duration", 60*time.Second, "traffic duration")
	flag.IntVar(&count, "count", 200, "burst count")
	flag.IntVar(&concurrency, "concurrency", 20, "burst concurrency")
	flag.Int64Var(&seed, "seed", 0, "override seed (0=use config)")
	flag.IntVar(&restaurants, "restaurants", 0, "override restaurant count")
	flag.IntVar(&riders, "riders", 0, "override rider count")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: gen <world|traffic|burst> [flags]")
		os.Exit(2)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		// try repo-root relative
		cfg, err = config.Load("../../configs/default.yaml")
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			os.Exit(1)
		}
	}
	if base == "" {
		base = cfg.Agent.BaseURL
	}
	adminToken := os.Getenv("ADMIN_TOKEN")
	client := &http.Client{Timeout: 15 * time.Second}
	switch flag.Arg(0) {
	case "world":
		body := map[string]any{}
		s := cfg.Generator.Seed
		if seed != 0 {
			s = seed
		}
		body["seed"] = s
		if restaurants > 0 {
			body["restaurants"] = restaurants
		}
		if riders > 0 {
			body["riders"] = riders
		}
		if err := postJSON(client, base+"/admin/seed/world", adminToken, body); err != nil {
			fmt.Fprintln(os.Stderr, "seed world:", err)
			os.Exit(1)
		}
		fmt.Println("world seeded")
	case "traffic":
		trafficSeed := seed
		if trafficSeed == 0 {
			trafficSeed = time.Now().UnixNano()
		}
		rng := rand.New(rand.NewSource(trafficSeed))
		rate := cfg.Generator.OrderRatePerMin
		if rate <= 0 {
			rate = 30
		}
		interval := time.Minute / time.Duration(rate)
		deadline := time.Now().Add(duration)
		for time.Now().Before(deadline) {
			priority := "normal"
			if rng.Float64() < cfg.Generator.VipRatio {
				priority = "vip"
			}
			_ = postOrder(client, base, priority)
			// periodically move a random rider
			if rng.Intn(5) == 0 {
				moveRandomRider(client, base, cfg, rng)
			}
			time.Sleep(interval)
		}
		fmt.Println("traffic done")
	case "burst":
		if concurrency <= 0 {
			concurrency = 1
		}
		if count < 0 {
			count = 0
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, concurrency)
		for i := 0; i < count; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				_ = postOrder(client, base, "normal")
			}()
		}
		wg.Wait()
		fmt.Println("burst done")
	default:
		fmt.Fprintln(os.Stderr, "unknown gen command")
		os.Exit(2)
	}
}

func postJSON(client *http.Client, url, adminToken string, body any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if adminToken != "" {
		req.Header.Set("X-Admin-Token", adminToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func postOrder(client *http.Client, base, priority string) error {
	b, _ := json.Marshal(map[string]any{"restaurant_picker": "random", "priority": priority})
	req, _ := http.NewRequest("POST", base+"/v1/orders", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Intn(1<<30)))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Order struct {
			ID string `json:"id"`
		} `json:"order"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Order.ID == "" {
		return nil
	}
	// drive lifecycle: prepare -> ready -> assign (best effort)
	for _, step := range []string{"prepare", "ready"} {
		r, _ := http.NewRequest("POST", base+"/v1/orders/"+out.Order.ID+"/"+step, nil)
		r.Header.Set("Idempotency-Key", fmt.Sprintf("%s-%s", step, out.Order.ID))
		if resp2, err := client.Do(r); err == nil {
			io.Copy(io.Discard, resp2.Body)
			resp2.Body.Close()
		}
	}
	r, _ := http.NewRequest("POST", base+"/v1/orders/"+out.Order.ID+"/assign", nil)
	r.Header.Set("Idempotency-Key", fmt.Sprintf("assign-%s", out.Order.ID))
	if resp2, err := client.Do(r); err == nil {
		io.Copy(io.Discard, resp2.Body)
		resp2.Body.Close()
	}
	return nil
}

func moveRandomRider(client *http.Client, base string, cfg config.Config, rng *rand.Rand) {
	// list riders
	resp, err := client.Get(base + "/v1/riders?status=available")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var out struct {
		Riders []struct {
			ID string `json:"id"`
		} `json:"riders"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Riders) == 0 {
		return
	}
	rd := out.Riders[rng.Intn(len(out.Riders))]
	bbox := cfg.Generator.CityBBox
	if len(bbox) != 4 {
		return
	}
	lat := bbox[0] + rng.Float64()*(bbox[2]-bbox[0])
	lng := bbox[1] + rng.Float64()*(bbox[3]-bbox[1])
	b, _ := json.Marshal(map[string]any{"lat": lat, "lng": lng})
	req, _ := http.NewRequest("POST", base+"/v1/riders/"+rd.ID+"/location", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if resp2, err := client.Do(req); err == nil {
		io.Copy(io.Discard, resp2.Body)
		resp2.Body.Close()
	}
}
