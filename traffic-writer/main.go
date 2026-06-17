// traffic-writer replays the AWS Honeypot ("marx-geo") dataset to drive the Outpost
// dashboard's 3D globe (echarts-gl arcs) and Sankey. It:
//
//   - streams individual attack events over WebSocket (/ws) in an accelerated,
//     continuous loop — rewriting each event's timestamp to ~now on every pass
//     so the stream never runs dry (the dataset is finite);
//   - serves the aggregated country->region flows as JSON (/aggregate) for the
//     globe's static weighted-arc base layer and the Sankey;
//   - optionally writes those aggregated flows to a ts-store schema store for
//     historical query (disabled when no API key is configured — WS-only mode).
//
// Mirrors the existing websocket/ and data-writer/ services. See
// docs/TRAFFIC-SIM-PLAN.md and docs/spike/.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Config holds runtime configuration (flags + env).
type Config struct {
	Port int
	// EventsPerSec is the accelerated replay rate over the WS stream.
	EventsPerSec int
	// ts-store aggregate writer (optional)
	TSStoreURL  string
	APIKey      string
	StoreName   string
	AggWriteSec int
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // allow all origins (test rig)
}

var (
	config  Config
	events  []Event
	flows   []Flow
	flowsMu sync.RWMutex // flows are static after load, but guarded for /aggregate consistency

	clients   = make(map[*websocket.Conn]bool)
	clientsMu sync.Mutex

	eventsPerSec int
	rateMu       sync.RWMutex
)

func main() {
	flag.IntVar(&config.Port, "port", getEnvInt("TRAFFIC_PORT", 8085), "WebSocket/HTTP port")
	flag.IntVar(&config.EventsPerSec, "events-per-sec", getEnvInt("TRAFFIC_EVENTS_PER_SEC", 50), "Accelerated replay rate (events/sec)")
	flag.StringVar(&config.TSStoreURL, "tsstore-url", getEnv("TSSTORE_URL", ""), "ts-store URL (empty disables the aggregate writer)")
	flag.StringVar(&config.APIKey, "api-key", getEnv("TSSTORE_API_KEY", ""), "ts-store API key for the aggregate store")
	flag.StringVar(&config.StoreName, "store-name", getEnv("TRAFFIC_STORE_NAME", "traffic-flows"), "ts-store schema store name for aggregated flows")
	flag.IntVar(&config.AggWriteSec, "agg-write-sec", getEnvInt("TRAFFIC_AGG_WRITE_SEC", 60), "Interval between aggregate writes to ts-store")
	flag.Parse()

	eventsPerSec = config.EventsPerSec

	events = loadEvents()
	if len(events) == 0 {
		log.Fatal("no events loaded; cannot start")
	}
	flows = aggregate(events)
	log.Printf("aggregated into %d country->region flows", len(flows))

	// Fail fast if the emitted record and the declared schema have drifted.
	if err := validateSchema(); err != nil {
		log.Fatalf("schema validation: %v", err)
	}

	// Start the accelerated WS replay loop.
	go broadcastEvents()

	// Start the ts-store aggregate writer if configured (otherwise WS-only).
	if config.TSStoreURL != "" && config.APIKey != "" {
		log.Printf("ts-store aggregate writer: enabled (store=%s, every %ds)", config.StoreName, config.AggWriteSec)
		go runAggregateWriter()
	} else {
		log.Printf("ts-store aggregate writer: disabled (set TSSTORE_URL + TSSTORE_API_KEY to enable)")
	}

	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/aggregate", handleAggregate)
	http.HandleFunc("/schema", handleSchema)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/health", handleHealth)

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("Traffic Simulator starting on %s", addr)
	log.Printf("  events=%d, flows=%d, rate=%d/sec", len(events), len(flows), config.EventsPerSec)
	log.Printf("  stream: ws://localhost%s/ws | aggregate: http://localhost%s/aggregate", addr, addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

// broadcastEvents replays events in a continuous accelerated loop. Each event's
// timestamp is rewritten to the moment it is emitted, so consumers always see
// "live" traffic and the finite dataset never runs dry. When no clients are
// connected we keep advancing the cursor cheaply so a new client joins mid-stream.
func broadcastEvents() {
	idx := 0
	for {
		rateMu.RLock()
		rate := eventsPerSec
		rateMu.RUnlock()
		if rate < 1 {
			rate = 1
		}
		time.Sleep(time.Second / time.Duration(rate))

		idx = (idx + 1) % len(events)

		clientsMu.Lock()
		if len(clients) == 0 {
			clientsMu.Unlock()
			continue
		}

		e := events[idx]
		e.Timestamp = time.Now().UnixMilli() // fresh timestamp on every pass
		data, err := json.Marshal(e)
		if err != nil {
			clientsMu.Unlock()
			log.Printf("marshal event: %v", err)
			continue
		}
		for c := range clients {
			if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
				c.Close()
				delete(clients, c)
			}
		}
		clientsMu.Unlock()
	}
}

// runAggregateWriter periodically writes the aggregated flows to a ts-store
// schema store. The store is expected to already exist (created by the deploy);
// mirrors data-writer's write pattern (X-API-Key, /api/stores/<name>/data).
func runAggregateWriter() {
	client := &http.Client{Timeout: 10 * time.Second}
	waitForTSStore(client, config.TSStoreURL)

	write := func() {
		flowsMu.RLock()
		snapshot := flows
		flowsMu.RUnlock()

		now := time.Now().UnixMilli()
		var written, errs int
		for _, f := range snapshot {
			record := flowRecord(f, now)
			if err := writeToTSStore(client, config.StoreName, record); err != nil {
				errs++
			} else {
				written++
			}
		}
		log.Printf("aggregate write: %d flows written, %d errors", written, errs)
	}

	write() // write once up front
	ticker := time.NewTicker(time.Duration(config.AggWriteSec) * time.Second)
	for range ticker.C {
		write()
	}
}

func writeToTSStore(client *http.Client, storeName string, record map[string]interface{}) error {
	body, err := json.Marshal(map[string]interface{}{"data": record})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/stores/%s/data", config.TSStoreURL, storeName)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", config.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func waitForTSStore(client *http.Client, baseURL string) {
	log.Printf("waiting for ts-store at %s ...", baseURL)
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

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade error: %v", err)
		return
	}
	clientsMu.Lock()
	clients[conn] = true
	n := len(clients)
	clientsMu.Unlock()
	log.Printf("client connected (total %d)", n)

	go func() {
		defer func() {
			clientsMu.Lock()
			delete(clients, conn)
			clientsMu.Unlock()
			conn.Close()
		}()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var cmd struct {
				Command      string `json:"command"`
				EventsPerSec int    `json:"events_per_sec"`
			}
			if json.Unmarshal(msg, &cmd) == nil && cmd.Command == "set_rate" && cmd.EventsPerSec > 0 {
				rateMu.Lock()
				eventsPerSec = cmd.EventsPerSec
				rateMu.Unlock()
				log.Printf("replay rate set to %d/sec", cmd.EventsPerSec)
			}
		}
	}()
}

// handleAggregate serves the static weighted flows (globe base layer + Sankey).
func handleAggregate(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	if r.Method == http.MethodOptions {
		return
	}
	flowsMu.RLock()
	snapshot := flows
	flowsMu.RUnlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"generated_at": time.Now().UnixMilli(),
		"count":        len(snapshot),
		"flows":        snapshot,
	})
}

// handleSchema serves the ts-store schema-store field contract. The trv-homelab
// role's `simulators_tsstore_schema_stores` block must declare exactly this set.
func handleSchema(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	if r.Method == http.MethodOptions {
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"store_name": config.StoreName,
		"data_type":  "schema",
		"fields":     trafficFlowSchema,
	})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method == http.MethodPost {
		var update struct {
			EventsPerSec int `json:"events_per_sec"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if update.EventsPerSec > 0 {
			rateMu.Lock()
			eventsPerSec = update.EventsPerSec
			rateMu.Unlock()
			log.Printf("replay rate set via HTTP to %d/sec", update.EventsPerSec)
		}
	}
	rateMu.RLock()
	rate := eventsPerSec
	rateMu.RUnlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"events_per_sec": rate,
		"total_events":   len(events),
		"total_flows":    len(flows),
		"store_name":     config.StoreName,
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	corsJSON(w)
	clientsMu.Lock()
	n := len(clients)
	clientsMu.Unlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "healthy",
		"connections": n,
		"events":      len(events),
		"uptime":      time.Now().Unix(),
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
