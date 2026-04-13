package main

import (
	"encoding/json"
	"flag"
	"fmt"
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

// Config holds server configuration
type Config struct {
	Port        int
	IntervalMs  int
	NumSensors  int
	EnableNoise bool
	AnomalyRate float64
}

// SensorSimulator generates realistic sensor data
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
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func main() {
	// Command line flags with environment variable defaults
	flag.IntVar(&config.Port, "port", getEnvInt("WS_PORT", 8081), "WebSocket server port")
	flag.IntVar(&config.IntervalMs, "interval", getEnvInt("WS_INTERVAL_MS", 1000), "Broadcast interval in milliseconds")
	flag.IntVar(&config.NumSensors, "sensors", getEnvInt("WS_NUM_SENSORS", 50), "Number of sensors")
	flag.BoolVar(&config.EnableNoise, "noise", getEnvBool("WS_ENABLE_NOISE", true), "Enable random noise")
	flag.Float64Var(&config.AnomalyRate, "anomaly-rate", getEnvFloat("WS_ANOMALY_RATE", 0.02), "Anomaly rate")
	flag.Parse()

	intervalMs = config.IntervalMs

	// Initialize sensors
	initSensors()

	// Start broadcasting
	go broadcastReadings()

	// HTTP handlers
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/health", handleHealth)

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("WebSocket Sensor Simulator starting on %s", addr)
	log.Printf("Configuration: interval=%dms, sensors=%d", config.IntervalMs, len(sensors))
	log.Printf("Connect via: ws://localhost%s/ws", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
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

	locations := []struct {
		name     string
		tempBase float64
	}{
		{"Building-A", 73.0},
		{"Building-B", 73.0},
		{"Warehouse", 80.0},
		{"Server-Room", 67.0},
		{"Manufacturing", 85.0},
	}

	// Create all sensor types in all locations
	sensors = make([]*SensorSimulator, 0, len(sensorTypes)*len(locations))
	sensorIndex := 0
	for _, loc := range locations {
		for _, st := range sensorTypes {
			sensorIndex++
			base := st.baseValue + (rand.Float64()*2 - 1)
			if st.sType == "temperature" {
				base = loc.tempBase + (rand.Float64()*2 - 1)
			}
			sensors = append(sensors, &SensorSimulator{
				id:         fmt.Sprintf("sensor-%03d", sensorIndex),
				sensorType: st.sType,
				unit:       st.unit,
				location:   loc.name,
				baseValue:  base,
				amplitude:  st.amplitude,
				noise:      st.noise,
				phase:      rand.Float64() * 2 * math.Pi,
			})
		}
	}

	log.Printf("Initialized %d sensors (%d types x %d locations)",
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

func broadcastReadings() {
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

		for _, sensor := range sensors {
			reading := sensor.generateReading()

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

	json.NewEncoder(w).Encode(map[string]interface{}{
		"interval_ms":  currentInterval,
		"num_sensors":  config.NumSensors,
		"noise":        config.EnableNoise,
		"anomaly_rate": config.AnomalyRate,
		"sensors":      len(sensors),
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	clientsMu.Lock()
	numClients := len(clients)
	clientsMu.Unlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "healthy",
		"connections": numClients,
		"uptime":      time.Now().Unix(),
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
