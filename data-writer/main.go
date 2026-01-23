package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"time"
)

// Config holds the data writer configuration
type Config struct {
	TSStoreURL   string
	StoreName    string
	APIKey       string
	NumSensors   int
	IntervalMS   int
	EnableNoise  bool
	AnomalyRate  float64
}

// SensorType defines a type of sensor with its characteristics
type SensorType struct {
	Name      string
	Unit      string
	BaseValue float64
	Amplitude float64
	Noise     float64
	Min       float64
	Max       float64
}

// SensorState tracks the current state of a sensor for smooth value transitions
type SensorState struct {
	ID            string
	Type          SensorType
	Location      string
	Phase         float64
	CurrentValue  float64
	AnomalyActive bool
	AnomalyEnd    time.Time
	AnomalyTarget float64
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

// TSStoreRequest is the request body for ts-store JSON endpoint
type TSStoreRequest struct {
	Data SensorReading `json:"data"`
}

var sensorTypes = []SensorType{
	// Temperature: 70°F base, slow drift between 68-72°F, extreme up to 85°F
	{"temperature", "°F", 70.0, 2.0, 0.1, 60.0, 90.0},
	{"humidity", "%", 50.0, 10.0, 1.0, 20.0, 80.0},
	{"pressure", "hPa", 1013.25, 5.0, 0.5, 990.0, 1030.0},
	{"co2", "ppm", 450.0, 50.0, 5.0, 350.0, 800.0},
	{"light", "lux", 400.0, 100.0, 10.0, 100.0, 1000.0},
	{"voltage", "V", 120.0, 2.0, 0.1, 115.0, 125.0},
	{"current", "A", 15.0, 3.0, 0.2, 5.0, 25.0},
	{"power", "W", 1800.0, 300.0, 15.0, 500.0, 3500.0},
	{"vibration", "mm/s", 2.5, 0.5, 0.1, 0.5, 5.0},
	{"flow_rate", "L/min", 75.0, 10.0, 1.5, 40.0, 120.0},
}

var locations = []string{
	"Building-A",
	"Building-B",
	"Warehouse",
	"Server-Room",
	"Manufacturing",
}

func main() {
	var config Config

	// Command line flags
	flag.StringVar(&config.TSStoreURL, "tsstore-url", getEnv("TSSTORE_URL", "http://localhost:8084"), "ts-store server URL")
	flag.StringVar(&config.StoreName, "store", getEnv("TSSTORE_STORE_NAME", "sensor-readings"), "ts-store store name")
	flag.StringVar(&config.APIKey, "api-key", getEnv("TSSTORE_API_KEY", ""), "ts-store API key")
	flag.IntVar(&config.IntervalMS, "interval", getEnvInt("INTERVAL_MS", 1000), "Interval between readings in milliseconds")
	flag.BoolVar(&config.EnableNoise, "noise", getEnvBool("ENABLE_NOISE", true), "Enable random noise")
	flag.Float64Var(&config.AnomalyRate, "anomaly-rate", getEnvFloat("ANOMALY_RATE", 0.002), "Anomaly rate (0-1)")
	flag.Parse()

	if config.APIKey == "" {
		log.Fatal("API key is required. Set TSSTORE_API_KEY environment variable or use -api-key flag")
	}

	rand.Seed(time.Now().UnixNano())

	// Initialize sensors: all types in all locations
	sensors := initializeSensors()
	log.Printf("Initialized %d sensors (%d types x %d locations)", len(sensors), len(sensorTypes), len(locations))

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	log.Printf("Starting data writer...")
	log.Printf("  ts-store URL: %s", config.TSStoreURL)
	log.Printf("  Store: %s", config.StoreName)
	log.Printf("  Interval: %dms", config.IntervalMS)
	log.Printf("  Noise: %v", config.EnableNoise)
	log.Printf("  Anomaly rate: %.3f", config.AnomalyRate)

	// Wait for ts-store to be ready
	waitForTSStore(client, config.TSStoreURL)

	// Track start time for drift calculations
	startTime := time.Now()
	ticker := time.NewTicker(time.Duration(config.IntervalMS) * time.Millisecond)

	// Stats
	var totalWritten, totalErrors int64
	lastStatsTime := time.Now()

	for range ticker.C {
		currentTime := time.Now()

		for _, sensor := range sensors {
			reading := generateReading(sensor, startTime, currentTime, config.EnableNoise, config.AnomalyRate)

			err := writeToTSStore(client, config, reading)
			if err != nil {
				totalErrors++
				if totalErrors%100 == 1 {
					log.Printf("Error writing to ts-store: %v", err)
				}
			} else {
				totalWritten++
			}
		}

		// Log stats every 30 seconds
		if time.Since(lastStatsTime) > 30*time.Second {
			log.Printf("Stats: written=%d, errors=%d, rate=%.1f/sec",
				totalWritten, totalErrors,
				float64(totalWritten)/time.Since(startTime).Seconds())
			lastStatsTime = time.Now()
		}
	}
}

func initializeSensors() []*SensorState {
	sensors := make([]*SensorState, 0, len(sensorTypes)*len(locations))

	sensorIndex := 0
	for _, loc := range locations {
		for _, st := range sensorTypes {
			sensorIndex++
			sensor := &SensorState{
				ID:           fmt.Sprintf("sensor-%03d", sensorIndex),
				Type:         st,
				Location:     loc,
				Phase:        rand.Float64() * 2 * math.Pi,
				CurrentValue: st.BaseValue,
			}
			sensors = append(sensors, sensor)
		}
	}

	return sensors
}

func generateReading(sensor *SensorState, startTime, currentTime time.Time, enableNoise bool, anomalyRate float64) SensorReading {
	hours := currentTime.Sub(startTime).Hours()

	// Very slow sinusoidal drift over 24-48 hour periods
	slowDrift := sensor.Type.Amplitude * math.Sin(hours/36+sensor.Phase)

	// Slight daily variation (smaller amplitude)
	dayPhase := (float64(currentTime.Hour()) / 24.0) * 2 * math.Pi
	dailyVar := (sensor.Type.Amplitude / 4) * math.Sin(dayPhase)

	// Calculate normal target value
	normalTarget := sensor.Type.BaseValue + slowDrift + dailyVar

	// Check if we should start a new anomaly
	if !sensor.AnomalyActive && rand.Float64() < anomalyRate {
		sensor.AnomalyActive = true
		// Anomaly lasts 3-10 minutes
		anomalyDuration := time.Duration(3+rand.Intn(8)) * time.Minute
		sensor.AnomalyEnd = currentTime.Add(anomalyDuration)

		// For temperature, spike up toward 85°F; for others, spike toward max
		if sensor.Type.Name == "temperature" {
			sensor.AnomalyTarget = 80.0 + rand.Float64()*5.0 // 80-85°F
		} else {
			// Spike to 70-90% of max range
			rangeSize := sensor.Type.Max - sensor.Type.Min
			sensor.AnomalyTarget = sensor.Type.BaseValue + rangeSize*0.3*(0.7+rand.Float64()*0.2)
		}
	}

	// Check if anomaly should end
	if sensor.AnomalyActive && currentTime.After(sensor.AnomalyEnd) {
		sensor.AnomalyActive = false
	}

	// Determine target value
	var targetValue float64
	if sensor.AnomalyActive {
		targetValue = sensor.AnomalyTarget
	} else {
		targetValue = normalTarget
	}

	// Smooth transition: move current value toward target
	driftRate := 0.1
	if sensor.AnomalyActive {
		driftRate = 0.15 // Slightly faster rise during anomaly
	}
	sensor.CurrentValue = sensor.CurrentValue + (targetValue-sensor.CurrentValue)*driftRate

	// Add tiny noise if enabled
	value := sensor.CurrentValue
	if enableNoise {
		value += (rand.Float64()*2 - 1) * sensor.Type.Noise
	}

	// Clamp to valid range
	value = math.Max(sensor.Type.Min, math.Min(sensor.Type.Max, value))
	value = math.Round(value*100) / 100

	// Determine status based on deviation from base
	status := "normal"
	quality := 95 + rand.Intn(6)

	deviation := math.Abs(value - sensor.Type.BaseValue)
	normalRange := sensor.Type.Amplitude * 1.5

	if deviation > normalRange*2 {
		status = "error"
		quality = 30 + rand.Intn(40)
	} else if deviation > normalRange {
		status = "warning"
		quality = 70 + rand.Intn(20)
	}

	return SensorReading{
		Timestamp:  currentTime.UnixMilli(),
		SensorID:   sensor.ID,
		SensorType: sensor.Type.Name,
		Value:      value,
		Unit:       sensor.Type.Unit,
		Location:   sensor.Location,
		Status:     status,
		Quality:    quality,
	}
}

func writeToTSStore(client *http.Client, config Config, reading SensorReading) error {
	reqBody := TSStoreRequest{Data: reading}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal reading: %w", err)
	}

	url := fmt.Sprintf("%s/api/stores/%s/json", config.TSStoreURL, config.StoreName)
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", config.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func waitForTSStore(client *http.Client, baseURL string) {
	log.Printf("Waiting for ts-store to be ready...")

	for {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Printf("ts-store is ready")
				return
			}
		}
		log.Printf("ts-store not ready, retrying in 5 seconds...")
		time.Sleep(5 * time.Second)
	}
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
