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
	TSStoreURL  string
	APIKey      string
	IntervalMS  int
	EnableNoise bool
	AnomalyRate float64
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
	Type          SensorType
	Phase         float64
	CurrentValue  float64
	AnomalyActive bool
	AnomalyEnd    time.Time
	AnomalyTarget float64
}

// Location represents a physical location with its own ts-store repo
type Location struct {
	Name      string // display name
	StoreName string // ts-store repo name
	Sensors   []*SensorState
}

// defaultSensorTypes uses a placeholder temperature base; each location overrides it
var sensorTypes = []SensorType{
	{"temperature", "°F", 0.0, 2.0, 0.1, 60.0, 95.0},
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

var locations = []struct {
	Name      string
	StoreName string
	TempBase  float64
}{
	{"Warehouse", "warehouse", 80.0},
	{"Server-Room", "server-room", 67.0},
	{"Manufacturing", "manufacturing", 85.0},
}

func main() {
	var config Config

	// Command line flags
	flag.StringVar(&config.TSStoreURL, "tsstore-url", getEnv("TSSTORE_URL", "http://localhost:8084"), "ts-store server URL")
	flag.StringVar(&config.APIKey, "api-key", getEnv("TSSTORE_API_KEY", ""), "ts-store API key")
	flag.IntVar(&config.IntervalMS, "interval", getEnvInt("INTERVAL_MS", 1000), "Interval between readings in milliseconds")
	flag.BoolVar(&config.EnableNoise, "noise", getEnvBool("ENABLE_NOISE", true), "Enable random noise")
	flag.Float64Var(&config.AnomalyRate, "anomaly-rate", getEnvFloat("ANOMALY_RATE", 0.002), "Anomaly rate (0-1)")
	flag.Parse()

	if config.APIKey == "" {
		log.Fatal("API key is required. Set TSSTORE_API_KEY environment variable or use -api-key flag")
	}

	rand.Seed(time.Now().UnixNano())

	// Initialize locations with sensors
	locs := initializeLocations()
	log.Printf("Initialized %d locations with %d sensor types each", len(locs), len(sensorTypes))

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	log.Printf("Starting data writer...")
	log.Printf("  ts-store URL: %s", config.TSStoreURL)
	log.Printf("  Stores: %v", func() []string {
		names := make([]string, len(locs))
		for i, l := range locs {
			names[i] = l.StoreName
		}
		return names
	}())
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

		for _, loc := range locs {
			record := generateRecord(loc, startTime, currentTime, config.EnableNoise, config.AnomalyRate)

			err := writeToTSStore(client, config, loc.StoreName, record)
			if err != nil {
				totalErrors++
				if totalErrors%100 == 1 {
					log.Printf("Error writing to ts-store (%s): %v", loc.StoreName, err)
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

func initializeLocations() []*Location {
	locs := make([]*Location, 0, len(locations))

	for _, locDef := range locations {
		loc := &Location{
			Name:      locDef.Name,
			StoreName: locDef.StoreName,
			Sensors:   make([]*SensorState, 0, len(sensorTypes)),
		}

		for _, st := range sensorTypes {
			sType := st
			if sType.Name == "temperature" {
				sType.BaseValue = locDef.TempBase
			}
			sensor := &SensorState{
				Type:         sType,
				Phase:        rand.Float64() * 2 * math.Pi,
				CurrentValue: sType.BaseValue,
			}
			loc.Sensors = append(loc.Sensors, sensor)
		}

		locs = append(locs, loc)
	}

	return locs
}

// generateRecord produces a single record with all sensor values for a location
func generateRecord(loc *Location, startTime, currentTime time.Time, enableNoise bool, anomalyRate float64) map[string]interface{} {
	record := map[string]interface{}{
		"timestamp": currentTime.UnixMilli(),
	}

	hours := currentTime.Sub(startTime).Hours()

	for _, sensor := range loc.Sensors {
		value := generateValue(sensor, hours, currentTime, enableNoise, anomalyRate)
		record[sensor.Type.Name] = value
	}

	return record
}

func generateValue(sensor *SensorState, hours float64, currentTime time.Time, enableNoise bool, anomalyRate float64) float64 {
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
		anomalyDuration := time.Duration(3+rand.Intn(8)) * time.Minute
		sensor.AnomalyEnd = currentTime.Add(anomalyDuration)

		if sensor.Type.Name == "temperature" {
			sensor.AnomalyTarget = 80.0 + rand.Float64()*5.0
		} else {
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

	// Smooth transition
	driftRate := 0.1
	if sensor.AnomalyActive {
		driftRate = 0.15
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

	return value
}

func writeToTSStore(client *http.Client, config Config, storeName string, record map[string]interface{}) error {
	reqBody := map[string]interface{}{"data": record}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	url := fmt.Sprintf("%s/api/stores/%s/data", config.TSStoreURL, storeName)
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
