// counter-writer emits raw, monotonically-increasing COUNTER columns to a
// ts-store schema store, as a test source for the Outpost dashboard's
// accumulator/delta transform (data_mapping.accumulator_columns +
// accumulator_reset_policy; see applyAccumulator in trv-outpost
// client/src/chart-spec/option-helpers.js).
//
// Unlike the other simulators here, this one does NOT emit instantaneous gauges
// — it emits accumulating totals, so the dashboard must subtract consecutive
// values to recover the per-interval delta. Because it is purpose-built for
// testing that transform, it can make a counter WRAP, RESET, SPIKE, go FLAT, or
// drop a GAP on demand (HTTP) or on a schedule — exercising every branch of the
// reset policy (drop_negative / keep_negative / clamp_zero) in seconds rather
// than waiting hours for a real /proc counter to roll over.
//
// Mirrors data-writer/: flag+env config, waitForTSStore, POST
// /api/stores/<name>/data with {"data": record} and X-API-Key.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"
)

// Config holds runtime configuration (flags + env).
type Config struct {
	TSStoreURL string
	APIKey     string
	StoreName  string
	IntervalMS int
	Port       int
}

var (
	config   Config
	counters []*Counter
	byName   map[string]*Counter
)

func main() {
	flag.StringVar(&config.TSStoreURL, "tsstore-url", getEnv("TSSTORE_URL", "http://localhost:8084"), "ts-store server URL")
	flag.StringVar(&config.APIKey, "api-key", getEnv("TSSTORE_API_KEY", ""), "ts-store API key")
	flag.StringVar(&config.StoreName, "store-name", getEnv("COUNTER_STORE_NAME", "counters"), "ts-store schema store name")
	flag.IntVar(&config.IntervalMS, "interval", getEnvInt("INTERVAL_MS", 1000), "Interval between counter writes (ms)")
	flag.IntVar(&config.Port, "port", getEnvInt("COUNTER_PORT", 8086), "HTTP control/config port")
	flag.Parse()

	if config.APIKey == "" {
		log.Fatal("API key is required. Set TSSTORE_API_KEY or use -api-key")
	}

	rand.Seed(time.Now().UnixNano())

	counters = defaultCounters()
	byName = make(map[string]*Counter, len(counters))
	for _, c := range counters {
		byName[c.Name] = c
	}

	// Fail fast if the emitted record and the declared schema have drifted.
	if err := validateSchema(); err != nil {
		log.Fatalf("schema validation: %v", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}

	log.Printf("Starting counter-writer...")
	log.Printf("  ts-store URL: %s", config.TSStoreURL)
	log.Printf("  store: %s", config.StoreName)
	log.Printf("  interval: %dms", config.IntervalMS)
	log.Printf("  columns: %d", len(counters))

	waitForTSStore(client, config.TSStoreURL)

	go runWriteLoop(client)
	startHTTP()
}

// runWriteLoop emits one record per tick containing every counter's current
// value. A counter in its GAP behavior is OMITTED from the record (null gap)
// rather than written as zero.
func runWriteLoop(client *http.Client) {
	ticker := time.NewTicker(time.Duration(config.IntervalMS) * time.Millisecond)
	start := time.Now()
	var written, errors int64
	lastStats := time.Now()

	for range ticker.C {
		record := map[string]interface{}{"timestamp": time.Now().UnixMilli()}
		for _, c := range counters {
			val, emit := c.tick()
			if emit {
				record[c.Name] = val
			}
			// emit==false → leave the field out (null/missing gap).
		}

		if err := writeToTSStore(client, config.StoreName, record); err != nil {
			errors++
			if errors%100 == 1 {
				log.Printf("write error (%s): %v", config.StoreName, err)
			}
		} else {
			written++
		}

		if time.Since(lastStats) > 30*time.Second {
			log.Printf("Stats: written=%d, errors=%d, rate=%.1f/sec",
				written, errors, float64(written)/time.Since(start).Seconds())
			lastStats = time.Now()
		}
	}
}

func writeToTSStore(client *http.Client, storeName string, record map[string]interface{}) error {
	body, err := json.Marshal(map[string]interface{}{"data": record})
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	url := fmt.Sprintf("%s/api/stores/%s/data", config.TSStoreURL, storeName)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", config.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func waitForTSStore(client *http.Client, baseURL string) {
	log.Printf("Waiting for ts-store at %s ...", baseURL)
	for {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Printf("ts-store is ready")
				return
			}
		}
		time.Sleep(5 * time.Second)
	}
}

func startHTTP() {
	http.HandleFunc("/trigger", handleTrigger)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/schema", handleSchema)
	http.HandleFunc("/health", handleHealth)

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("control HTTP on %s  (POST /trigger {\"column\":\"bytes_total\",\"behavior\":\"wrap\"})", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

// handleTrigger queues a one-shot behavior on a column for the next tick.
// Body: {"column":"<name>","behavior":"ramp|wrap|reset|spike|flat|gap"}.
// Omit "column" (or pass "*"/"all") to apply to every counter.
func handleTrigger(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Column   string `json:"column"`
		Behavior string `json:"behavior"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b := Behavior(req.Behavior)
	if !validBehavior(b) {
		http.Error(w, "unknown behavior: "+req.Behavior, http.StatusBadRequest)
		return
	}

	var applied []string
	if req.Column == "" || req.Column == "*" || req.Column == "all" {
		for _, c := range counters {
			c.trigger(b)
			applied = append(applied, c.Name)
		}
	} else {
		c, ok := byName[req.Column]
		if !ok {
			http.Error(w, "unknown column: "+req.Column, http.StatusBadRequest)
			return
		}
		c.trigger(b)
		applied = append(applied, c.Name)
	}
	log.Printf("trigger %s on %v", b, applied)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"behavior": req.Behavior,
		"columns":  applied,
		"applied":  "next-tick",
	})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	if r.Method == http.MethodOptions {
		return
	}
	cols := make([]map[string]interface{}, 0, len(counters))
	for _, c := range counters {
		cols = append(cols, map[string]interface{}{
			"name":          c.Name,
			"value":         c.snapshot(),
			"step":          c.Step,
			"max":           c.Max,
			"auto_every":    c.AutoEvery,
			"auto_behavior": c.AutoBehavior,
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"store_name":  config.StoreName,
		"interval_ms": config.IntervalMS,
		"behaviors":   []Behavior{BehaviorRamp, BehaviorWrap, BehaviorReset, BehaviorSpike, BehaviorFlat, BehaviorGap},
		"columns":     cols,
	})
}

// handleSchema serves the ts-store schema-store field contract — the set the
// deploy's schema-store declaration must match.
func handleSchema(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	if r.Method == http.MethodOptions {
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"store_name": config.StoreName,
		"data_type":  "schema",
		"fields":     counterSchema,
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "healthy",
		"columns": len(counters),
		"uptime":  time.Now().Unix(),
	})
}

func corsJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// env helpers (match the other services)
func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	return def
}
