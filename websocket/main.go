package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
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
	Timestamp   int64   `json:"timestamp"`
	SensorID    string  `json:"sensor_id"`
	SensorType  string  `json:"sensor_type"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Location    string  `json:"location"`
	Status      string  `json:"status"`
	Quality     int     `json:"quality"` // 0-100
}

// Config holds server configuration
type Config struct {
	Port           int
	IntervalMs     int
	NumSensors     int
	EnableNoise    bool
	AnomalyRate    float64
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

var sensors []*SensorSimulator
var config Config
var clients = make(map[*websocket.Conn]bool)
var clientsMu sync.Mutex
var intervalMs int
var intervalMu sync.RWMutex

func init() {
	rand.Seed(time.Now().UnixNano())
}

func main() {
	flag.IntVar(&config.Port, "port", 8081, "WebSocket server port")
	flag.IntVar(&config.IntervalMs, "interval", 1000, "Sensor reading interval in milliseconds")
	flag.IntVar(&config.NumSensors, "sensors", 5, "Number of sensors to simulate")
	flag.BoolVar(&config.EnableNoise, "noise", true, "Enable random noise in readings")
	flag.Float64Var(&config.AnomalyRate, "anomaly-rate", 0.02, "Rate of anomalous readings (0-1)")
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
	log.Printf("Configuration: interval=%dms, sensors=%d, noise=%v, anomaly_rate=%.2f",
		config.IntervalMs, config.NumSensors, config.EnableNoise, config.AnomalyRate)
	log.Printf("Connect via: ws://localhost%s/ws", addr)
	log.Printf("Config endpoint: http://localhost%s/config", addr)

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
		{"temperature", "°C", 22.0, 5.0, 0.5},
		{"humidity", "%", 45.0, 15.0, 2.0},
		{"pressure", "hPa", 1013.25, 10.0, 1.0},
		{"co2", "ppm", 400.0, 100.0, 10.0},
		{"light", "lux", 500.0, 300.0, 20.0},
		{"voltage", "V", 120.0, 5.0, 0.1},
		{"current", "A", 10.0, 3.0, 0.2},
		{"power", "W", 1200.0, 400.0, 10.0},
		{"vibration", "mm/s", 2.0, 1.5, 0.3},
		{"flow_rate", "L/min", 50.0, 20.0, 2.0},
	}

	locations := []string{
		"Building-A/Floor-1",
		"Building-A/Floor-2",
		"Building-B/Floor-1",
		"Building-B/Floor-2",
		"Warehouse/Zone-1",
		"Warehouse/Zone-2",
		"Server-Room",
		"Manufacturing/Line-1",
		"Manufacturing/Line-2",
		"External",
	}

	sensors = make([]*SensorSimulator, config.NumSensors)
	for i := 0; i < config.NumSensors; i++ {
		st := sensorTypes[i%len(sensorTypes)]
		sensors[i] = &SensorSimulator{
			id:         fmt.Sprintf("sensor-%03d", i+1),
			sensorType: st.sType,
			unit:       st.unit,
			location:   locations[i%len(locations)],
			baseValue:  st.baseValue,
			amplitude:  st.amplitude,
			noise:      st.noise,
			phase:      rand.Float64() * 2 * math.Pi,
		}
	}
}

func (s *SensorSimulator) generateReading() SensorReading {
	now := time.Now()

	// Base sinusoidal pattern (simulates daily cycles)
	t := float64(now.Unix()) / 3600.0 // hour-based cycle
	value := s.baseValue + s.amplitude*math.Sin(t*0.5+s.phase)

	// Add noise if enabled
	if config.EnableNoise {
		value += (rand.Float64()*2 - 1) * s.noise
	}

	// Determine status and quality
	status := "normal"
	quality := 95 + rand.Intn(6) // 95-100

	// Generate anomalies
	if rand.Float64() < config.AnomalyRate {
		anomalyType := rand.Intn(3)
		switch anomalyType {
		case 0: // Spike
			value *= 1.5 + rand.Float64()*0.5
			status = "warning"
			quality = 70 + rand.Intn(20)
		case 1: // Drop
			value *= 0.3 + rand.Float64()*0.2
			status = "warning"
			quality = 70 + rand.Intn(20)
		case 2: // Sensor fault
			status = "error"
			quality = rand.Intn(50)
		}
	}

	return SensorReading{
		Timestamp:  now.UnixMilli(),
		SensorID:   s.id,
		SensorType: s.sensorType,
		Value:      math.Round(value*100) / 100, // 2 decimal places
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

		// Generate readings from all sensors
		for _, sensor := range sensors {
			reading := sensor.generateReading()
			data, err := json.Marshal(reading)
			if err != nil {
				log.Printf("Error marshaling reading: %v", err)
				continue
			}

			// Broadcast to all connected clients
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

	// Handle incoming messages (for configuration updates)
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

			// Check for interval update command
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
