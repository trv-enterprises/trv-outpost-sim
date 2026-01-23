package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for testing
	},
}

// SensorReading represents a single sensor data point
type SensorReading struct {
	Timestamp  int64   `json:"timestamp"`
	SensorID   string  `json:"sensor_id"`
	SensorType string  `json:"sensor_type"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	Location   string  `json:"location"`
	Status     string  `json:"status"`
	Quality    int     `json:"quality"`
}

// TSStoreResponse represents the response from ts-store JSON endpoint
type TSStoreResponse struct {
	Objects []TSStoreObject `json:"objects"`
	Count   int             `json:"count"`
}

// TSStoreObject represents a single object from ts-store
type TSStoreObject struct {
	Timestamp       int64         `json:"timestamp"`
	PrimaryBlockNum int           `json:"primary_block_num"`
	TotalSize       int           `json:"total_size"`
	BlockCount      int           `json:"block_count"`
	Data            SensorReading `json:"data"`
}

// Config holds server configuration
type Config struct {
	Port        int
	IntervalMs  int
	TSStoreURL  string
	StoreName   string
	APIKey      string
	NumSensors  int
	EnableNoise bool
	AnomalyRate float64
}

// SensorSimulator generates realistic sensor data (fallback mode)
type SensorSimulator struct {
	id         string
	sensorType string
	unit       string
	location   string
	baseValue  float64
	amplitude  float64
	noise      float64
	phase      float64
}

var (
	sensors    []*SensorSimulator
	config     Config
	clients    = make(map[*websocket.Conn]bool)
	clientsMu  sync.Mutex
	intervalMs int
	intervalMu sync.RWMutex
	httpClient *http.Client

	// Track ts-store availability
	tsStoreAvailable   bool
	tsStoreAvailableMu sync.RWMutex
	lastTSStoreCheck   time.Time

	// Track last seen timestamp to avoid duplicates
	lastSeenTimestamp   int64
	lastSeenTimestampMu sync.Mutex
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func main() {
	// Command line flags with environment variable defaults
	flag.IntVar(&config.Port, "port", getEnvInt("WS_PORT", 8081), "WebSocket server port")
	flag.IntVar(&config.IntervalMs, "interval", getEnvInt("WS_INTERVAL_MS", 1000), "Broadcast interval in milliseconds")
	flag.StringVar(&config.TSStoreURL, "tsstore-url", getEnv("TSSTORE_URL", ""), "ts-store server URL (empty to disable)")
	flag.StringVar(&config.StoreName, "store", getEnv("TSSTORE_STORE_NAME", "sensor-readings"), "ts-store store name")
	flag.StringVar(&config.APIKey, "api-key", getEnv("TSSTORE_API_KEY", ""), "ts-store API key")
	flag.IntVar(&config.NumSensors, "sensors", getEnvInt("WS_NUM_SENSORS", 50), "Number of sensors (fallback mode)")
	flag.BoolVar(&config.EnableNoise, "noise", getEnvBool("WS_ENABLE_NOISE", true), "Enable random noise (fallback mode)")
	flag.Float64Var(&config.AnomalyRate, "anomaly-rate", getEnvFloat("WS_ANOMALY_RATE", 0.02), "Anomaly rate (fallback mode)")
	flag.Parse()

	intervalMs = config.IntervalMs

	// Initialize HTTP client for ts-store
	httpClient = &http.Client{
		Timeout: 5 * time.Second,
	}

	// Check if ts-store is configured and available
	if config.TSStoreURL != "" && config.APIKey != "" {
		log.Printf("ts-store configured: %s (store: %s)", config.TSStoreURL, config.StoreName)
		checkTSStoreAvailability()
	} else {
		log.Printf("ts-store not configured, using fallback data generation")
		tsStoreAvailable = false
	}

	// Initialize fallback sensors
	initSensors()

	// Start broadcasting
	go broadcastReadings()

	// Start periodic ts-store health check
	go periodicTSStoreCheck()

	// HTTP handlers
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/health", handleHealth)

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("WebSocket Sensor Simulator starting on %s", addr)
	log.Printf("Configuration: interval=%dms, sensors=%d", config.IntervalMs, config.NumSensors)
	log.Printf("Connect via: ws://localhost%s/ws", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func checkTSStoreAvailability() {
	url := fmt.Sprintf("%s/health", config.TSStoreURL)
	resp, err := httpClient.Get(url)
	if err != nil {
		tsStoreAvailableMu.Lock()
		tsStoreAvailable = false
		tsStoreAvailableMu.Unlock()
		log.Printf("ts-store not available: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		tsStoreAvailableMu.Lock()
		tsStoreAvailable = true
		tsStoreAvailableMu.Unlock()
		log.Printf("ts-store is available")
	} else {
		tsStoreAvailableMu.Lock()
		tsStoreAvailable = false
		tsStoreAvailableMu.Unlock()
		log.Printf("ts-store returned status: %d", resp.StatusCode)
	}
	lastTSStoreCheck = time.Now()
}

func periodicTSStoreCheck() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		if config.TSStoreURL != "" && config.APIKey != "" {
			checkTSStoreAvailability()
		}
	}
}

func fetchFromTSStore() ([]SensorReading, error) {
	url := fmt.Sprintf("%s/api/stores/%s/json/newest?limit=%d",
		config.TSStoreURL, config.StoreName, config.NumSensors)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-API-Key", config.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch from ts-store: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ts-store returned status %d: %s", resp.StatusCode, string(body))
	}

	var tsResp TSStoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&tsResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Extract readings from response
	readings := make([]SensorReading, 0, len(tsResp.Objects))
	for _, obj := range tsResp.Objects {
		readings = append(readings, obj.Data)
	}

	return readings, nil
}

func initSensors() {
	sensorTypes := []struct {
		sType     string
		unit      string
		baseValue float64
		amplitude float64
		noise     float64
	}{
		{"temperature", "°F", 70.0, 2.0, 0.1},
		{"humidity", "%", 50.0, 10.0, 1.0},
		{"pressure", "hPa", 1013.25, 5.0, 0.5},
		{"co2", "ppm", 450.0, 50.0, 5.0},
		{"light", "lux", 400.0, 100.0, 10.0},
		{"voltage", "V", 120.0, 2.0, 0.1},
		{"current", "A", 15.0, 3.0, 0.2},
		{"power", "W", 1800.0, 300.0, 15.0},
		{"vibration", "mm/s", 2.5, 0.5, 0.1},
		{"flow_rate", "L/min", 75.0, 10.0, 1.5},
	}

	locations := []string{
		"Building-A",
		"Building-B",
		"Warehouse",
		"Server-Room",
		"Manufacturing",
	}

	// Create all sensor types in all locations (matching data-writer)
	sensors = make([]*SensorSimulator, 0, len(sensorTypes)*len(locations))
	sensorIndex := 0
	for _, loc := range locations {
		for _, st := range sensorTypes {
			sensorIndex++
			sensors = append(sensors, &SensorSimulator{
				id:         fmt.Sprintf("sensor-%03d", sensorIndex),
				sensorType: st.sType,
				unit:       st.unit,
				location:   loc,
				baseValue:  st.baseValue + (rand.Float64()*2 - 1),
				amplitude:  st.amplitude,
				noise:      st.noise,
				phase:      rand.Float64() * 2 * math.Pi,
			})
		}
	}

	log.Printf("Initialized %d fallback sensors (%d types x %d locations)",
		len(sensors), len(sensorTypes), len(locations))
}

func (s *SensorSimulator) generateReading() SensorReading {
	now := time.Now()

	t := float64(now.Unix()) / 3600.0
	value := s.baseValue + s.amplitude*math.Sin(t*0.5+s.phase)

	if config.EnableNoise {
		value += (rand.Float64()*2 - 1) * s.noise
	}

	status := "normal"
	quality := 95 + rand.Intn(6)

	if rand.Float64() < config.AnomalyRate {
		anomalyType := rand.Intn(3)
		switch anomalyType {
		case 0:
			value *= 1.5 + rand.Float64()*0.5
			status = "warning"
			quality = 70 + rand.Intn(20)
		case 1:
			value *= 0.3 + rand.Float64()*0.2
			status = "warning"
			quality = 70 + rand.Intn(20)
		case 2:
			status = "error"
			quality = rand.Intn(50)
		}
	}

	return SensorReading{
		Timestamp:  now.UnixMilli(),
		SensorID:   s.id,
		SensorType: s.sensorType,
		Value:      math.Round(value*100) / 100,
		Unit:       s.unit,
		Location:   s.location,
		Status:     status,
		Quality:    quality,
	}
}

func generateFallbackReadings() []SensorReading {
	readings := make([]SensorReading, len(sensors))
	for i, sensor := range sensors {
		readings[i] = sensor.generateReading()
	}
	return readings
}

func broadcastReadings() {
	var consecutiveErrors int

	for {
		intervalMu.RLock()
		interval := intervalMs
		intervalMu.RUnlock()

		time.Sleep(time.Duration(interval) * time.Millisecond)

		clientsMu.Lock()
		if len(clients) == 0 {
			clientsMu.Unlock()
			continue
		}

		var readings []SensorReading
		var source string

		// Try to fetch from ts-store first
		tsStoreAvailableMu.RLock()
		useTS := tsStoreAvailable
		tsStoreAvailableMu.RUnlock()

		if useTS {
			var err error
			readings, err = fetchFromTSStore()
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors == 1 || consecutiveErrors%10 == 0 {
					log.Printf("Error fetching from ts-store (count=%d): %v", consecutiveErrors, err)
				}
				// Fall back to generated data
				readings = generateFallbackReadings()
				source = "fallback"
			} else {
				consecutiveErrors = 0
				source = "ts-store"

				// Filter out readings we've already sent (avoid duplicates)
				lastSeenTimestampMu.Lock()
				filteredReadings := make([]SensorReading, 0, len(readings))
				var maxTimestamp int64
				for _, r := range readings {
					if r.Timestamp > lastSeenTimestamp {
						filteredReadings = append(filteredReadings, r)
					}
					if r.Timestamp > maxTimestamp {
						maxTimestamp = r.Timestamp
					}
				}
				if maxTimestamp > lastSeenTimestamp {
					lastSeenTimestamp = maxTimestamp
				}
				lastSeenTimestampMu.Unlock()

				readings = filteredReadings

				// If no new readings, skip this cycle
				if len(readings) == 0 {
					clientsMu.Unlock()
					continue
				}
			}
		} else {
			readings = generateFallbackReadings()
			source = "fallback"
		}

		// Log source periodically
		if rand.Float64() < 0.01 { // 1% of broadcasts
			log.Printf("Broadcasting %d readings from %s to %d clients", len(readings), source, len(clients))
		}

		// Broadcast readings to all connected clients
		for _, reading := range readings {
			data, err := json.Marshal(reading)
			if err != nil {
				log.Printf("Error marshaling reading: %v", err)
				continue
			}

			for client := range clients {
				err := client.WriteMessage(websocket.TextMessage, data)
				if err != nil {
					log.Printf("Error sending to client: %v", err)
					client.Close()
					delete(clients, client)
				}
			}
		}
		clientsMu.Unlock()
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}

	clientsMu.Lock()
	clients[conn] = true
	clientsMu.Unlock()

	log.Printf("New client connected. Total clients: %d", len(clients))

	go func() {
		defer func() {
			clientsMu.Lock()
			delete(clients, conn)
			clientsMu.Unlock()
			conn.Close()
			log.Printf("Client disconnected. Total clients: %d", len(clients))
		}()

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var cmd struct {
				Command  string `json:"command"`
				Interval int    `json:"interval"`
			}
			if err := json.Unmarshal(message, &cmd); err == nil {
				if cmd.Command == "set_interval" && cmd.Interval > 0 {
					intervalMu.Lock()
					intervalMs = cmd.Interval
					intervalMu.Unlock()
					log.Printf("Interval updated to %dms", cmd.Interval)
				}
			}
		}
	}()
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		return
	}

	if r.Method == "POST" {
		var update struct {
			IntervalMs int `json:"interval_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if update.IntervalMs > 0 {
			intervalMu.Lock()
			intervalMs = update.IntervalMs
			intervalMu.Unlock()
			log.Printf("Interval updated via HTTP to %dms", update.IntervalMs)
		}
	}

	intervalMu.RLock()
	currentInterval := intervalMs
	intervalMu.RUnlock()

	tsStoreAvailableMu.RLock()
	tsAvail := tsStoreAvailable
	tsStoreAvailableMu.RUnlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"interval_ms":      currentInterval,
		"num_sensors":      config.NumSensors,
		"noise":            config.EnableNoise,
		"anomaly_rate":     config.AnomalyRate,
		"sensors":          len(sensors),
		"tsstore_url":      config.TSStoreURL,
		"tsstore_available": tsAvail,
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	clientsMu.Lock()
	numClients := len(clients)
	clientsMu.Unlock()

	tsStoreAvailableMu.RLock()
	tsAvail := tsStoreAvailable
	tsStoreAvailableMu.RUnlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "healthy",
		"connections":      numClients,
		"uptime":           time.Now().Unix(),
		"tsstore_available": tsAvail,
	})
}

// Helper functions for environment variables
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var result int
		fmt.Sscanf(value, "%d", &result)
		return result
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return value == "true" || value == "1"
	}
	return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		var result float64
		fmt.Sscanf(value, "%f", &result)
		return result
	}
	return defaultValue
}
