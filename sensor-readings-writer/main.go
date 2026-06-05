// sensor-readings-writer emits the combined, row-per-sensor "tidy" dataset to a
// single ts-store schema store named `sensor-readings`.
//
// This is the shape of the original combined store that was split into three
// per-location stores on 2026-04-12 (warehouse / server-room / manufacturing)
// because the flat-per-location model aggregates better in the dashboard. Those
// three stores still exist and are written by the sibling `data-writer`; this
// service runs ALONGSIDE them and does NOT touch them — it exists to exercise
// the row-per-sensor shape (location + sensor as dimension fields, one value
// per record) against ts-store.
//
// One record per sensor per tick: 5 locations x 10 sensor types = 50 records/sec.
// Record fields match the schema declared in the simulators Ansible role
// (timestamp, sensor_id, sensor_type, value, unit, location, status, quality).
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
	"strconv"
	"time"
)

const storeName = "sensor-readings"

type Config struct {
	TSStoreURL  string
	APIKey      string
	IntervalMS  int
	EnableNoise bool
	AnomalyRate float64
}

// SensorType defines a type of sensor with its characteristics.
type SensorType struct {
	Name      string
	Unit      string
	BaseValue float64
	Amplitude float64
	Noise     float64
	Min       float64
	Max       float64
}

// SensorState tracks the current state of a single sensor for smooth transitions.
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

// SensorReading is one record written to the sensor-readings store. JSON field
// names MUST match the schema field names registered by the Ansible role.
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

// sensorTypes mirrors the sibling data-writer's catalogue. Temperature base is a
// placeholder; each location overrides it (see locations below).
var sensorTypes = []SensorType{
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

// locations are the five dimension values carried in each record's `location`
// field. TempBase shifts each location's temperature sensor baseline.
var locations = []struct {
	Name     string
	TempBase float64
}{
	{"Building-A", 73.0},
	{"Building-B", 73.0},
	{"Warehouse", 80.0},
	{"Server-Room", 67.0},
	{"Manufacturing", 85.0},
}

func main() {
	var config Config

	flag.StringVar(&config.TSStoreURL, "tsstore-url", getEnv("TSSTORE_URL", "http://localhost:8080"), "ts-store server URL")
	flag.StringVar(&config.APIKey, "api-key", getEnv("TSSTORE_API_KEY", ""), "ts-store API key")
	flag.IntVar(&config.IntervalMS, "interval", getEnvInt("INTERVAL_MS", 1000), "Interval between reading batches in milliseconds")
	flag.BoolVar(&config.EnableNoise, "noise", getEnvBool("ENABLE_NOISE", true), "Enable random noise")
	flag.Float64Var(&config.AnomalyRate, "anomaly-rate", getEnvFloat("ANOMALY_RATE", 0.002), "Anomaly rate (0-1)")
	flag.Parse()

	if config.APIKey == "" {
		log.Fatal("API key is required. Set TSSTORE_API_KEY environment variable or use -api-key flag")
	}

	rand.Seed(time.Now().UnixNano())

	sensors := initializeSensors()
	log.Printf("Initialized %d sensors (%d types x %d locations)", len(sensors), len(sensorTypes), len(locations))

	client := &http.Client{Timeout: 10 * time.Second}

	log.Printf("Starting sensor-readings writer...")
	log.Printf("  ts-store URL: %s", config.TSStoreURL)
	log.Printf("  Store: %s", storeName)
	log.Printf("  Interval: %dms", config.IntervalMS)
	log.Printf("  Noise: %v", config.EnableNoise)
	log.Printf("  Anomaly rate: %.3f", config.AnomalyRate)

	waitForTSStore(client, config.TSStoreURL)

	startTime := time.Now()
	ticker := time.NewTicker(time.Duration(config.IntervalMS) * time.Millisecond)

	var totalWritten, totalErrors int64
	lastStatsTime := time.Now()

	for range ticker.C {
		currentTime := time.Now()
		hours := currentTime.Sub(startTime).Hours()

		for _, sensor := range sensors {
			reading := generateReading(sensor, hours, currentTime, config.EnableNoise, config.AnomalyRate)

			if err := writeToTSStore(client, config, reading); err != nil {
				totalErrors++
				if totalErrors%100 == 1 {
					log.Printf("Error writing to ts-store: %v", err)
				}
			} else {
				totalWritten++
			}
		}

		if time.Since(lastStatsTime) > 30*time.Second {
			log.Printf("Stats: written=%d, errors=%d, rate=%.1f/sec",
				totalWritten, totalErrors,
				float64(totalWritten)/time.Since(startTime).Seconds())
			lastStatsTime = time.Now()
		}
	}
}

// initializeSensors builds one sensor per (location, type) pair — the full
// fan-out, so every location carries every sensor type.
func initializeSensors() []*SensorState {
	sensors := make([]*SensorState, 0, len(sensorTypes)*len(locations))

	for _, loc := range locations {
		for _, st := range sensorTypes {
			sType := st
			if sType.Name == "temperature" {
				sType.BaseValue = loc.TempBase
			}
			sensors = append(sensors, &SensorState{
				ID:           fmt.Sprintf("%s-%s", loc.Name, sType.Name),
				Type:         sType,
				Location:     loc.Name,
				Phase:        rand.Float64() * 2 * math.Pi,
				CurrentValue: sType.BaseValue,
			})
		}
	}

	return sensors
}

// generateReading advances a sensor's state and produces one record.
func generateReading(sensor *SensorState, hours float64, currentTime time.Time, enableNoise bool, anomalyRate float64) SensorReading {
	value := generateValue(sensor, hours, currentTime, enableNoise, anomalyRate)

	status := "normal"
	quality := 95 + rand.Intn(6) // 95-100 when healthy
	if sensor.AnomalyActive {
		status = "anomaly"
		quality = 40 + rand.Intn(30) // 40-69 during an anomaly
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

// generateValue is the same smooth-drift + occasional-anomaly model the sibling
// data-writer uses, so both stores show consistent-looking signals.
func generateValue(sensor *SensorState, hours float64, currentTime time.Time, enableNoise bool, anomalyRate float64) float64 {
	slowDrift := sensor.Type.Amplitude * math.Sin(hours/36+sensor.Phase)

	dayPhase := (float64(currentTime.Hour()) / 24.0) * 2 * math.Pi
	dailyVar := (sensor.Type.Amplitude / 4) * math.Sin(dayPhase)

	normalTarget := sensor.Type.BaseValue + slowDrift + dailyVar

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

	if sensor.AnomalyActive && currentTime.After(sensor.AnomalyEnd) {
		sensor.AnomalyActive = false
	}

	targetValue := normalTarget
	if sensor.AnomalyActive {
		targetValue = sensor.AnomalyTarget
	}

	driftRate := 0.1
	if sensor.AnomalyActive {
		driftRate = 0.15
	}
	sensor.CurrentValue += (targetValue - sensor.CurrentValue) * driftRate

	value := sensor.CurrentValue
	if enableNoise {
		value += (rand.Float64()*2 - 1) * sensor.Type.Noise
	}

	value = math.Max(sensor.Type.Min, math.Min(sensor.Type.Max, value))
	value = math.Round(value*100) / 100

	return value
}

func writeToTSStore(client *http.Client, config Config, reading SensorReading) error {
	reqBody := map[string]interface{}{"data": reading}
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

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
