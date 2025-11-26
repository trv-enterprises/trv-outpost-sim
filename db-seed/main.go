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
	{"temperature", "°C", 22.0, 8.0, 0.5, -20.0, 60.0},
	{"humidity", "%", 50.0, 20.0, 2.0, 0.0, 100.0},
	{"pressure", "hPa", 1013.25, 15.0, 1.0, 970.0, 1050.0},
	{"co2", "ppm", 450.0, 150.0, 15.0, 300.0, 2000.0},
	{"light", "lux", 400.0, 350.0, 25.0, 0.0, 2000.0},
	{"voltage", "V", 120.0, 3.0, 0.2, 110.0, 130.0},
	{"current", "A", 15.0, 5.0, 0.3, 0.0, 30.0},
	{"power", "W", 1800.0, 600.0, 20.0, 0.0, 5000.0},
	{"vibration", "mm/s", 2.5, 1.5, 0.2, 0.0, 10.0},
	{"flow_rate", "L/min", 75.0, 25.0, 3.0, 0.0, 200.0},
}

var locations = []string{
	"Building-A/Floor-1",
	"Building-A/Floor-2",
	"Building-A/Floor-3",
	"Building-B/Floor-1",
	"Building-B/Floor-2",
	"Warehouse/Zone-1",
	"Warehouse/Zone-2",
	"Warehouse/Zone-3",
	"Server-Room/Rack-1",
	"Server-Room/Rack-2",
	"Manufacturing/Line-1",
	"Manufacturing/Line-2",
	"External/North",
	"External/South",
	"HVAC/Unit-1",
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

	// Create sensors
	log.Printf("Creating %d sensors...", config.NumSensors)
	sensors := make([]struct {
		id        string
		sType     sensorType
		location  string
		phase     float64
	}, config.NumSensors)

	for i := 0; i < config.NumSensors; i++ {
		sensorID := fmt.Sprintf("sensor-%03d", i+1)
		st := sensorTypes[i%len(sensorTypes)]
		loc := locations[i%len(locations)]
		phase := rand.Float64() * 2 * math.Pi

		sensors[i] = struct {
			id        string
			sType     sensorType
			location  string
			phase     float64
		}{sensorID, st, loc, phase}

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

	// Generate readings
	endTime := time.Now()
	startTime := endTime.AddDate(0, 0, -config.DaysBack)
	totalReadings := int(endTime.Sub(startTime).Seconds()) / config.Interval * config.NumSensors

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
			// Generate value with sinusoidal pattern
			hours := currentTime.Sub(startTime).Hours()
			dayPhase := (float64(currentTime.Hour()) / 24.0) * 2 * math.Pi // Daily cycle

			value := sensor.sType.baseValue +
				sensor.sType.amplitude*math.Sin(hours/12+sensor.phase) + // Long-term trend
				(sensor.sType.amplitude/2)*math.Sin(dayPhase) +           // Daily cycle
				(rand.Float64()*2-1)*sensor.sType.noise                    // Noise

			// Clamp to valid range
			value = math.Max(sensor.sType.min, math.Min(sensor.sType.max, value))
			value = math.Round(value*100) / 100

			// Determine status
			status := "normal"
			quality := 95 + rand.Intn(6)

			rangePercent := (value - sensor.sType.min) / (sensor.sType.max - sensor.sType.min)
			if rangePercent < 0.1 || rangePercent > 0.9 {
				status = "warning"
				quality = 70 + rand.Intn(20)
			}

			// Random anomalies (2% chance)
			if rand.Float64() < 0.02 {
				status = "error"
				quality = 30 + rand.Intn(40)
				value *= 0.5 + rand.Float64() // Anomalous value
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
