// +build ignore

// Script to generate larger CSV files for testing
// Usage: go run scripts/generate-csv.go -output data/large_dataset.csv -rows 10000

package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"time"
)

type sensorType struct {
	name      string
	unit      string
	baseValue float64
	amplitude float64
	noise     float64
}

var sensorTypes = []sensorType{
	{"temperature", "°C", 22.0, 8.0, 0.5},
	{"humidity", "%", 50.0, 20.0, 2.0},
	{"pressure", "hPa", 1013.25, 15.0, 1.0},
	{"co2", "ppm", 450.0, 150.0, 15.0},
	{"light", "lux", 400.0, 350.0, 25.0},
}

var locations = []string{
	"Building-A/Floor-1",
	"Building-A/Floor-2",
	"Building-B/Floor-1",
	"Warehouse/Zone-1",
	"Server-Room",
}

func main() {
	output := flag.String("output", "data/sensor_readings.csv", "Output file path")
	rows := flag.Int("rows", 1000, "Number of rows to generate")
	sensors := flag.Int("sensors", 5, "Number of sensors")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	file, err := os.Create(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	writer.Write([]string{
		"timestamp", "sensor_id", "sensor_type", "value", "unit",
		"location", "quality", "status",
	})

	// Generate data
	startTime := time.Now().Add(-time.Duration(*rows/(*sensors)) * 15 * time.Minute)
	rowsPerSensor := *rows / *sensors

	for i := 0; i < *sensors; i++ {
		st := sensorTypes[i%len(sensorTypes)]
		sensorID := fmt.Sprintf("sensor-%03d", i+1)
		location := locations[i%len(locations)]
		phase := rand.Float64() * 2 * math.Pi

		for j := 0; j < rowsPerSensor; j++ {
			timestamp := startTime.Add(time.Duration(j) * 15 * time.Minute)

			// Generate value
			t := float64(timestamp.Unix()) / 3600.0
			value := st.baseValue + st.amplitude*math.Sin(t*0.5+phase)
			value += (rand.Float64()*2 - 1) * st.noise
			value = math.Round(value*100) / 100

			// Status and quality
			status := "normal"
			quality := 95 + rand.Intn(6)

			if rand.Float64() < 0.02 {
				status = "warning"
				quality = 70 + rand.Intn(20)
			}
			if rand.Float64() < 0.005 {
				status = "error"
				quality = 30 + rand.Intn(40)
			}

			writer.Write([]string{
				timestamp.Format(time.RFC3339),
				sensorID,
				st.name,
				fmt.Sprintf("%.2f", value),
				st.unit,
				location,
				fmt.Sprintf("%d", quality),
				status,
			})
		}
	}

	fmt.Printf("Generated %d rows to %s\n", *rows, *output)
}
