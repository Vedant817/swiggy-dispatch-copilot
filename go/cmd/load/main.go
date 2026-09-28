// Command load drives concurrent create/assign traffic against a live API and
// reports latency percentiles with methodology (warm-up, samples, failures,
// machine context) to load/report.json.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p / 100 * float64(len(sorted)-1))
	return sorted[i]
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "API base URL")
	concurrency := flag.Int("concurrency", 10, "parallel workers")
	count := flag.Int("count", 100, "measured samples (after warm-up)")
	warmup := flag.Int("warmup", 10, "warm-up iterations")
	out := flag.String("out", "../load/report.json", "report path")
	flag.Parse()

	client := &http.Client{Timeout: 15 * time.Second}
	post := func(path string, body any, key string) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequest("POST", *base+path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		t := time.Now()
		resp, err := client.Do(req)
		_ = t
		if err != nil {
			return -1, nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// Outcome classes: ok (offered), rejected (409 no capacity — expected
	// backpressure under fixed fleet), failed (5xx/connection/protocol).
	one := func() (createMs, assignMs float64, outcome string) {
		t0 := time.Now()
		code, b := post("/v1/orders", map[string]any{"restaurant_picker": "random", "priority": "normal"}, uuid.NewString())
		createMs = time.Since(t0).Seconds() * 1000
		if code != 201 {
			return createMs, 0, "failed"
		}
		var out struct {
			Order struct {
				ID string `json:"id"`
			} `json:"order"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.Order.ID == "" {
			return createMs, 0, "failed"
		}
		oid := out.Order.ID
		for _, s := range []string{"prepare", "ready"} {
			if code, _ := post("/v1/orders/"+oid+"/"+s, map[string]any{}, s+"-"+oid); code != 200 {
				return createMs, 0, "failed"
			}
		}
		t1 := time.Now()
		code, b2 := post("/v1/orders/"+oid+"/assign", map[string]any{}, "assign-"+oid)
		assignMs = time.Since(t1).Seconds() * 1000
		if code == 201 {
			return createMs, assignMs, "ok"
		}
		if code == 409 {
			return createMs, assignMs, "rejected"
		}
		_ = b2
		return createMs, assignMs, "failed"
	}

	for i := 0; i < *warmup; i++ {
		one()
	}
	var mu sync.Mutex
	var creates, assigns []float64
	failures, rejected := 0, 0
	jobs := make(chan struct{}, *count)
	for i := 0; i < *count; i++ {
		jobs <- struct{}{}
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				c, a, outcome := one()
				mu.Lock()
				switch outcome {
				case "ok":
					creates = append(creates, c)
					assigns = append(assigns, a)
				case "rejected":
					rejected++
				default:
					failures++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Float64s(creates)
	sort.Float64s(assigns)
	report := map[string]any{
		"base":            *base,
		"concurrency":     *concurrency,
		"samples":         len(creates),
		"warmup":          *warmup,
		"rejected_no_capacity": rejected,
		"failures":        failures,
		"create_p50_ms":   pct(creates, 50),
		"create_p95_ms":   pct(creates, 95),
		"assign_p50_ms":   pct(assigns, 50),
		"assign_p95_ms":   pct(assigns, 95),
		"go_version":      runtime.Version(),
		"os_arch":         runtime.GOOS + "/" + runtime.GOARCH,
		"measured_at":     time.Now().UTC().Format(time.RFC3339),
		"server_mode":     "go run dev (single instance, in-process worker)",
		"threshold_check": map[string]bool{"create_p95_lt_100": pct(creates, 95) < 100, "assign_p95_lt_500": pct(assigns, 95) < 500},
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	_ = os.WriteFile(*out, b, 0644)
	fmt.Println(string(b))
	if failures > 0 || pct(creates, 95) >= 100 || pct(assigns, 95) >= 500 {
		os.Exit(1)
	}
}
