// Package obs provides structured JSON logging and in-memory counters.
package obs

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"sync/atomic"
)

var logger = log.New(os.Stdout, "", 0)

func Log(event string, fields map[string]any) {
	m := map[string]any{"event": event}
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	logger.Println(string(b))
}

type Counters struct {
	mu sync.Mutex
	m  map[string]*atomic.Int64
}

func NewCounters() *Counters { return &Counters{m: map[string]*atomic.Int64{}} }

func (c *Counters) Inc(name string) {
	c.mu.Lock()
	ctr, ok := c.m[name]
	if !ok {
		ctr = &atomic.Int64{}
		c.m[name] = ctr
	}
	c.mu.Unlock()
	ctr.Add(1)
}

func (c *Counters) Get(name string) int64 {
	c.mu.Lock()
	ctr, ok := c.m[name]
	c.mu.Unlock()
	if !ok {
		return 0
	}
	return ctr.Load()
}

func (c *Counters) Snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.m))
	for k, v := range c.m {
		out[k] = v.Load()
	}
	return out
}
