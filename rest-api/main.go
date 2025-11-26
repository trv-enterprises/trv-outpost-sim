package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// SensorReading represents a single sensor data point
type SensorReading struct {
	ID          int64   `json:"id"`
	Timestamp   string  `json:"timestamp"`
	TimestampMs int64   `json:"timestamp_ms"`
	SensorID    string  `json:"sensor_id"`
	SensorType  string  `json:"sensor_type"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Location    string  `json:"location"`
	Status      string  `json:"status"`
	Quality     int     `json:"quality"`
}

// SensorInfo represents sensor metadata
type SensorInfo struct {
	SensorID    string  `json:"sensor_id"`
	SensorType  string  `json:"sensor_type"`
	Unit        string  `json:"unit"`
	Location    string  `json:"location"`
	MinValue    float64 `json:"min_value"`
	MaxValue    float64 `json:"max_value"`
	LastReading float64 `json:"last_reading"`
	LastUpdate  string  `json:"last_update"`
}

// Config holds server configuration
type Config struct {
	Port          int
	BufferSize    int
	GenerateMs    int
}

var (
	config       Config
	readings     []SensorReading
	readingsMu   sync.RWMutex
	sensors      []SensorInfo
	idCounter    int64
	sensorStates map[string]*sensorState
)

type sensorState struct {
	baseValue float64
	amplitude float64
	noise     float64
	phase     float64
	min       float64
	max       float64
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func main() {
	flag.IntVar(&config.Port, "port", 8082, "REST API server port")
	flag.IntVar(&config.BufferSize, "buffer", 1000, "Number of readings to keep in memory")
	flag.IntVar(&config.GenerateMs, "generate", 5000, "Generate new readings every N milliseconds")
	flag.Parse()

	initSensors()
	go generateReadings()

	r := mux.NewRouter()

	// Apply CORS middleware
	r.Use(corsMiddleware)

	// API endpoints
	r.HandleFunc("/api/readings", getReadings).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/readings/latest", getLatestReadings).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/readings/{sensor_id}", getReadingsBySensor).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/sensors", getSensors).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/sensors/{sensor_id}", getSensorInfo).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/stats", getStats).Methods("GET", "OPTIONS")
	r.HandleFunc("/health", handleHealth).Methods("GET", "OPTIONS")

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("REST API Sensor Simulator starting on %s", addr)
	log.Printf("Configuration: buffer=%d, generate_interval=%dms", config.BufferSize, config.GenerateMs)
	log.Printf("Endpoints:")
	log.Printf("  GET /api/readings          - All readings (supports ?limit=N&offset=N&sensor_id=X)")
	log.Printf("  GET /api/readings/latest   - Latest reading from each sensor")
	log.Printf("  GET /api/readings/{id}     - Readings for specific sensor")
	log.Printf("  GET /api/sensors           - List all sensors")
	log.Printf("  GET /api/sensors/{id}      - Sensor details")
	log.Printf("  GET /api/stats             - Statistics")

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func initSensors() {
	sensorTypes := []struct {
		sType     string
		unit      string
		baseValue float64
		amplitude float64
		noise     float64
		min       float64
		max       float64
	}{
		{"temperature", "°C", 22.0, 5.0, 0.5, -10.0, 50.0},
		{"humidity", "%", 45.0, 15.0, 2.0, 0.0, 100.0},
		{"pressure", "hPa", 1013.25, 10.0, 1.0, 980.0, 1050.0},
		{"co2", "ppm", 400.0, 100.0, 10.0, 300.0, 1000.0},
		{"light", "lux", 500.0, 300.0, 20.0, 0.0, 2000.0},
	}

	locations := []string{
		"Building-A/Floor-1",
		"Building-A/Floor-2",
		"Building-B/Floor-1",
		"Warehouse/Zone-1",
		"Server-Room",
	}

	sensorStates = make(map[string]*sensorState)
	sensors = make([]SensorInfo, 0)

	for i, st := range sensorTypes {
		sensorID := fmt.Sprintf("sensor-%03d", i+1)
		phase := rand.Float64() * 2 * math.Pi

		sensorStates[sensorID] = &sensorState{
			baseValue: st.baseValue,
			amplitude: st.amplitude,
			noise:     st.noise,
			phase:     phase,
			min:       st.min,
			max:       st.max,
		}

		sensors = append(sensors, SensorInfo{
			SensorID:   sensorID,
			SensorType: st.sType,
			Unit:       st.unit,
			Location:   locations[i%len(locations)],
			MinValue:   st.min,
			MaxValue:   st.max,
		})
	}

	readings = make([]SensorReading, 0, config.BufferSize)
}

func generateReading(sensor SensorInfo, state *sensorState) SensorReading {
	now := time.Now()
	idCounter++

	// Base sinusoidal pattern
	t := float64(now.Unix()) / 3600.0
	value := state.baseValue + state.amplitude*math.Sin(t*0.5+state.phase)

	// Add noise
	value += (rand.Float64()*2 - 1) * state.noise

	// Clamp to valid range
	value = math.Max(state.min, math.Min(state.max, value))

	// Status based on value range
	status := "normal"
	quality := 95 + rand.Intn(6)

	rangePercent := (value - state.min) / (state.max - state.min)
	if rangePercent < 0.1 || rangePercent > 0.9 {
		status = "warning"
		quality = 70 + rand.Intn(20)
	}

	return SensorReading{
		ID:          idCounter,
		Timestamp:   now.Format(time.RFC3339),
		TimestampMs: now.UnixMilli(),
		SensorID:    sensor.SensorID,
		SensorType:  sensor.SensorType,
		Value:       math.Round(value*100) / 100,
		Unit:        sensor.Unit,
		Location:    sensor.Location,
		Status:      status,
		Quality:     quality,
	}
}

func generateReadings() {
	// Generate some historical data first
	for i := 0; i < 100; i++ {
		for _, sensor := range sensors {
			state := sensorStates[sensor.SensorID]
			reading := generateReading(sensor, state)
			readings = append(readings, reading)
		}
	}

	ticker := time.NewTicker(time.Duration(config.GenerateMs) * time.Millisecond)
	for range ticker.C {
		readingsMu.Lock()
		for i := range sensors {
			state := sensorStates[sensors[i].SensorID]
			reading := generateReading(sensors[i], state)

			// Update sensor last reading
			sensors[i].LastReading = reading.Value
			sensors[i].LastUpdate = reading.Timestamp

			readings = append(readings, reading)

			// Trim buffer if needed
			if len(readings) > config.BufferSize {
				readings = readings[len(readings)-config.BufferSize:]
			}
		}
		readingsMu.Unlock()
	}
}

func getReadings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse query parameters
	limit := 100
	offset := 0
	sensorID := r.URL.Query().Get("sensor_id")
	startTime := r.URL.Query().Get("start_time")
	endTime := r.URL.Query().Get("end_time")

	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	readingsMu.RLock()
	defer readingsMu.RUnlock()

	// Filter readings
	filtered := make([]SensorReading, 0)
	for _, reading := range readings {
		// Filter by sensor ID
		if sensorID != "" && reading.SensorID != sensorID {
			continue
		}
		// Filter by time range
		if startTime != "" {
			if start, err := time.Parse(time.RFC3339, startTime); err == nil {
				readingTime, _ := time.Parse(time.RFC3339, reading.Timestamp)
				if readingTime.Before(start) {
					continue
				}
			}
		}
		if endTime != "" {
			if end, err := time.Parse(time.RFC3339, endTime); err == nil {
				readingTime, _ := time.Parse(time.RFC3339, reading.Timestamp)
				if readingTime.After(end) {
					continue
				}
			}
		}
		filtered = append(filtered, reading)
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].TimestampMs > filtered[j].TimestampMs
	})

	// Apply pagination
	total := len(filtered)
	if offset >= total {
		filtered = []SensorReading{}
	} else {
		end := offset + limit
		if end > total {
			end = total
		}
		filtered = filtered[offset:end]
	}

	response := map[string]interface{}{
		"data":   filtered,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}

	json.NewEncoder(w).Encode(response)
}

func getLatestReadings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	readingsMu.RLock()
	defer readingsMu.RUnlock()

	// Get latest reading for each sensor
	latest := make(map[string]SensorReading)
	for _, reading := range readings {
		if existing, ok := latest[reading.SensorID]; !ok || reading.TimestampMs > existing.TimestampMs {
			latest[reading.SensorID] = reading
		}
	}

	result := make([]SensorReading, 0, len(latest))
	for _, reading := range latest {
		result = append(result, reading)
	}

	// Sort by sensor ID
	sort.Slice(result, func(i, j int) bool {
		return result[i].SensorID < result[j].SensorID
	})

	json.NewEncoder(w).Encode(result)
}

func getReadingsBySensor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	vars := mux.Vars(r)
	sensorID := vars["sensor_id"]

	// Parse limit
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	readingsMu.RLock()
	defer readingsMu.RUnlock()

	filtered := make([]SensorReading, 0)
	for _, reading := range readings {
		if reading.SensorID == sensorID {
			filtered = append(filtered, reading)
		}
	}

	// Sort by timestamp descending
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].TimestampMs > filtered[j].TimestampMs
	})

	// Apply limit
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	if len(filtered) == 0 {
		http.Error(w, "Sensor not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(filtered)
}

func getSensors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sensors)
}

func getSensorInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	vars := mux.Vars(r)
	sensorID := vars["sensor_id"]

	for _, sensor := range sensors {
		if sensor.SensorID == sensorID {
			json.NewEncoder(w).Encode(sensor)
			return
		}
	}

	http.Error(w, "Sensor not found", http.StatusNotFound)
}

func getStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	readingsMu.RLock()
	defer readingsMu.RUnlock()

	// Calculate stats per sensor
	stats := make(map[string]map[string]interface{})
	for _, sensor := range sensors {
		values := make([]float64, 0)
		var statusCounts = map[string]int{"normal": 0, "warning": 0, "error": 0}

		for _, reading := range readings {
			if reading.SensorID == sensor.SensorID {
				values = append(values, reading.Value)
				statusCounts[reading.Status]++
			}
		}

		if len(values) > 0 {
			var sum, min, max float64
			min = values[0]
			max = values[0]
			for _, v := range values {
				sum += v
				if v < min {
					min = v
				}
				if v > max {
					max = v
				}
			}
			avg := sum / float64(len(values))

			stats[sensor.SensorID] = map[string]interface{}{
				"count":         len(values),
				"min":           math.Round(min*100) / 100,
				"max":           math.Round(max*100) / 100,
				"avg":           math.Round(avg*100) / 100,
				"status_counts": statusCounts,
			}
		}
	}

	response := map[string]interface{}{
		"total_readings": len(readings),
		"sensor_count":   len(sensors),
		"stats":          stats,
		"generated_at":   time.Now().Format(time.RFC3339),
	}

	json.NewEncoder(w).Encode(response)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	readingsMu.RLock()
	numReadings := len(readings)
	readingsMu.RUnlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "healthy",
		"readings":      numReadings,
		"sensors":       len(sensors),
		"buffer_size":   config.BufferSize,
		"generate_ms":   config.GenerateMs,
	})
}
