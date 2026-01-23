package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"time"

	_ "github.com/lib/pq"
)

// Config holds seeder configuration
type Config struct {
	Host       string
	Port       int
	User       string
	Password   string
	DBName     string
	NumSensors int
	DaysBack   int
	Interval   int // seconds between readings
}

type sensorType struct {
	name      string
	unit      string
	baseValue float64
	amplitude float64
	noise     float64
	min       float64
	max       float64
}

var sensorTypes = []sensorType{
	// Temperature: 70°F base (~21°C), slow drift between 68-72°F (20-22°C), extreme up to 85°F (~29°C)
	// Using Fahrenheit values directly since unit is °F
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
	flag.StringVar(&config.Host, "host", "localhost", "Database host")
	flag.IntVar(&config.Port, "port", 5432, "Database port")
	flag.StringVar(&config.User, "user", "postgres", "Database user")
	flag.StringVar(&config.Password, "password", "postgres", "Database password")
	flag.StringVar(&config.DBName, "dbname", "sensors", "Database name")
	flag.IntVar(&config.NumSensors, "sensors", 10, "Number of sensors")
	flag.IntVar(&config.DaysBack, "days", 30, "Days of historical data")
	flag.IntVar(&config.Interval, "interval", 60, "Seconds between readings")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		config.Host, config.Port, config.User, config.Password, config.DBName)

	log.Printf("Connecting to PostgreSQL at %s:%d...", config.Host, config.Port)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	log.Println("Connected successfully!")

	// Create schema
	if err := createSchema(db); err != nil {
		log.Fatalf("Failed to create schema: %v", err)
	}

	// Seed data
	if err := seedData(db, config); err != nil {
		log.Fatalf("Failed to seed data: %v", err)
	}

	log.Println("Database seeding completed!")
}

func createSchema(db *sql.DB) error {
	log.Println("Creating schema...")

	// Create sensors table
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sensors (
			sensor_id VARCHAR(50) PRIMARY KEY,
			sensor_type VARCHAR(50) NOT NULL,
			unit VARCHAR(20) NOT NULL,
			location VARCHAR(100) NOT NULL,
			min_value DECIMAL(10,2),
			max_value DECIMAL(10,2),
			installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			is_active BOOLEAN DEFAULT true
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create sensors table: %w", err)
	}

	// Create readings table with time-based partitioning support
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS sensor_readings (
			id BIGSERIAL,
			timestamp TIMESTAMP NOT NULL,
			sensor_id VARCHAR(50) NOT NULL,
			value DECIMAL(12,4) NOT NULL,
			quality INTEGER DEFAULT 100,
			status VARCHAR(20) DEFAULT 'normal',
			PRIMARY KEY (id, timestamp)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create sensor_readings table: %w", err)
	}

	// Create indexes for efficient querying
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_readings_timestamp ON sensor_readings(timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_readings_sensor_id ON sensor_readings(sensor_id)`,
		`CREATE INDEX IF NOT EXISTS idx_readings_sensor_time ON sensor_readings(sensor_id, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_readings_status ON sensor_readings(status)`,
	}

	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			log.Printf("Warning: index creation issue: %v", err)
		}
	}

	// Create aggregated stats view
	_, err = db.Exec(`
		CREATE OR REPLACE VIEW sensor_stats AS
		SELECT
			sensor_id,
			DATE_TRUNC('hour', timestamp) as hour,
			COUNT(*) as reading_count,
			AVG(value) as avg_value,
			MIN(value) as min_value,
			MAX(value) as max_value,
			AVG(quality) as avg_quality
		FROM sensor_readings
		GROUP BY sensor_id, DATE_TRUNC('hour', timestamp)
	`)
	if err != nil {
		log.Printf("Warning: failed to create view: %v", err)
	}

	// Create latest readings view
	_, err = db.Exec(`
		CREATE OR REPLACE VIEW latest_readings AS
		SELECT DISTINCT ON (sensor_id)
			r.id,
			r.timestamp,
			r.sensor_id,
			s.sensor_type,
			r.value,
			s.unit,
			s.location,
			r.quality,
			r.status
		FROM sensor_readings r
		JOIN sensors s ON r.sensor_id = s.sensor_id
		ORDER BY sensor_id, timestamp DESC
	`)
	if err != nil {
		log.Printf("Warning: failed to create latest_readings view: %v", err)
	}

	log.Println("Schema created successfully")
	return nil
}

func seedData(db *sql.DB, config Config) error {
	// Clear existing data
	log.Println("Clearing existing data...")
	_, _ = db.Exec("TRUNCATE TABLE sensor_readings CASCADE")
	_, _ = db.Exec("TRUNCATE TABLE sensors CASCADE")

	// Create sensors: all sensor types in each location
	// This creates len(sensorTypes) * len(locations) sensors (e.g., 10 types * 5 locations = 50 sensors)
	numSensors := len(sensorTypes) * len(locations)
	log.Printf("Creating %d sensors (%d types x %d locations)...", numSensors, len(sensorTypes), len(locations))

	type sensorState struct {
		id              string
		sType           sensorType
		location        string
		phase           float64
		currentValue    float64   // Track current value for smooth drift
		anomalyActive   bool      // Is an anomaly event happening?
		anomalyEnd      time.Time // When does the anomaly end?
		anomalyTarget   float64   // Target value during anomaly
	}

	sensors := make([]*sensorState, 0, numSensors)

	sensorIndex := 0
	for _, loc := range locations {
		for _, st := range sensorTypes {
			sensorIndex++
			sensorID := fmt.Sprintf("sensor-%03d", sensorIndex)
			phase := rand.Float64() * 2 * math.Pi

			sensors = append(sensors, &sensorState{
				id:           sensorID,
				sType:        st,
				location:     loc,
				phase:        phase,
				currentValue: st.baseValue,
			})

			_, err := db.Exec(`
				INSERT INTO sensors (sensor_id, sensor_type, unit, location, min_value, max_value)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (sensor_id) DO UPDATE SET
					sensor_type = EXCLUDED.sensor_type,
					unit = EXCLUDED.unit,
					location = EXCLUDED.location
			`, sensorID, st.name, st.unit, loc, st.min, st.max)
			if err != nil {
				return fmt.Errorf("failed to insert sensor %s: %w", sensorID, err)
			}
		}
	}

	// Generate readings
	endTime := time.Now()
	startTime := endTime.AddDate(0, 0, -config.DaysBack)
	totalReadings := int(endTime.Sub(startTime).Seconds()) / config.Interval * numSensors

	log.Printf("Generating %d readings (%d days of data)...", totalReadings, config.DaysBack)

	// Prepare batch insert
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO sensor_readings (timestamp, sensor_id, value, quality, status)
		VALUES ($1, $2, $3, $4, $5)
	`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to prepare statement: %w", err)
	}

	batchSize := 1000
	count := 0
	currentTime := startTime

	for currentTime.Before(endTime) {
		for _, sensor := range sensors {
			// Calculate target value based on slow drift pattern
			hours := currentTime.Sub(startTime).Hours()

			// Very slow sinusoidal drift over 24-48 hour periods
			slowDrift := sensor.sType.amplitude * math.Sin(hours/36+sensor.phase)

			// Slight daily variation (smaller amplitude)
			dayPhase := (float64(currentTime.Hour()) / 24.0) * 2 * math.Pi
			dailyVar := (sensor.sType.amplitude / 4) * math.Sin(dayPhase)

			// Calculate normal target value
			normalTarget := sensor.sType.baseValue + slowDrift + dailyVar

			// Check if we should start a new anomaly (0.2% chance per reading, ~1-2 per day per sensor)
			if !sensor.anomalyActive && rand.Float64() < 0.002 {
				sensor.anomalyActive = true
				// Anomaly lasts 3-10 minutes
				anomalyDuration := time.Duration(3+rand.Intn(8)) * time.Minute
				sensor.anomalyEnd = currentTime.Add(anomalyDuration)

				// For temperature, spike up toward 85°F; for others, spike toward max
				if sensor.sType.name == "temperature" {
					sensor.anomalyTarget = 80.0 + rand.Float64()*5.0 // 80-85°F
				} else {
					// Spike to 70-90% of max range
					rangeSize := sensor.sType.max - sensor.sType.min
					sensor.anomalyTarget = sensor.sType.baseValue + rangeSize*0.3*(0.7+rand.Float64()*0.2)
				}
			}

			// Check if anomaly should end
			if sensor.anomalyActive && currentTime.After(sensor.anomalyEnd) {
				sensor.anomalyActive = false
			}

			// Determine target value
			var targetValue float64
			if sensor.anomalyActive {
				targetValue = sensor.anomalyTarget
			} else {
				targetValue = normalTarget
			}

			// Smooth transition: move current value toward target (drift speed)
			// Move ~10% of the way toward target each reading for smooth transitions
			driftRate := 0.1
			if sensor.anomalyActive {
				driftRate = 0.15 // Slightly faster rise during anomaly
			}
			sensor.currentValue = sensor.currentValue + (targetValue-sensor.currentValue)*driftRate

			// Add tiny noise
			value := sensor.currentValue + (rand.Float64()*2-1)*sensor.sType.noise

			// Clamp to valid range
			value = math.Max(sensor.sType.min, math.Min(sensor.sType.max, value))
			value = math.Round(value*100) / 100

			// Determine status based on deviation from base
			status := "normal"
			quality := 95 + rand.Intn(6)

			deviation := math.Abs(value - sensor.sType.baseValue)
			normalRange := sensor.sType.amplitude * 1.5 // Values within 1.5x amplitude are normal

			if deviation > normalRange*2 {
				status = "error"
				quality = 30 + rand.Intn(40)
			} else if deviation > normalRange {
				status = "warning"
				quality = 70 + rand.Intn(20)
			}

			_, err := stmt.Exec(currentTime, sensor.id, value, quality, status)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to insert reading: %w", err)
			}

			count++
			if count%batchSize == 0 {
				// Commit batch
				if err := tx.Commit(); err != nil {
					return fmt.Errorf("failed to commit batch: %w", err)
				}
				log.Printf("Inserted %d readings...", count)

				// Start new transaction
				tx, err = db.Begin()
				if err != nil {
					return fmt.Errorf("failed to start new transaction: %w", err)
				}
				stmt, err = tx.Prepare(`
					INSERT INTO sensor_readings (timestamp, sensor_id, value, quality, status)
					VALUES ($1, $2, $3, $4, $5)
				`)
				if err != nil {
					tx.Rollback()
					return fmt.Errorf("failed to prepare statement: %w", err)
				}
			}
		}

		currentTime = currentTime.Add(time.Duration(config.Interval) * time.Second)
	}

	// Final commit
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit final batch: %w", err)
	}

	log.Printf("Successfully inserted %d readings", count)

	// Analyze tables for query optimization
	log.Println("Analyzing tables...")
	db.Exec("ANALYZE sensors")
	db.Exec("ANALYZE sensor_readings")

	return nil
}
