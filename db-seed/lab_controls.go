package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"math/rand"
	"time"
)

type labControl struct {
	id         string
	name       string
	analyte    string
	unit       string
	targetMean float64
	targetSD   float64
	n          int
}

var labControls = []labControl{
	{"LC-GLU-1", "Glucose Level 1", "glucose", "mg/dL", 100.0, 5.0, 6},
	{"LC-GLU-2", "Glucose Level 2", "glucose", "mg/dL", 250.0, 10.0, 6},
	{"LC-CHOL-1", "Cholesterol Level 1", "cholesterol", "mg/dL", 180.0, 8.0, 6},
}

func createLabControlSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS lab_controls (
			control_id   VARCHAR(50) PRIMARY KEY,
			name         VARCHAR(100) NOT NULL,
			analyte      VARCHAR(100) NOT NULL,
			unit         VARCHAR(20)  NOT NULL,
			target_mean  DECIMAL(12,4) NOT NULL,
			target_sd    DECIMAL(12,4) NOT NULL,
			is_active    BOOLEAN DEFAULT true,
			created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("create lab_controls: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS lab_control_daily (
			control_id   VARCHAR(50)  NOT NULL REFERENCES lab_controls(control_id) ON DELETE CASCADE,
			day          DATE         NOT NULL,
			n            INTEGER      NOT NULL,
			mean         DECIMAL(12,4) NOT NULL,
			sd           DECIMAL(12,4) NOT NULL,
			minus_2sd    DECIMAL(12,4) NOT NULL,
			minus_1sd    DECIMAL(12,4) NOT NULL,
			plus_1sd     DECIMAL(12,4) NOT NULL,
			plus_2sd     DECIMAL(12,4) NOT NULL,
			PRIMARY KEY (control_id, day)
		)
	`)
	if err != nil {
		return fmt.Errorf("create lab_control_daily: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_lab_control_daily_day ON lab_control_daily(day DESC)`)
	if err != nil {
		log.Printf("Warning: lab_control_daily index: %v", err)
	}

	return nil
}

// generateDailyAggregate returns a synthetic (n, mean, sd) for one day.
// Mean centers near target, SD floats near target_sd, with ~5% chance of an outlier day.
func generateDailyAggregate(c labControl, day time.Time) (int, float64, float64) {
	_ = day
	outlier := rand.Float64() < 0.05

	// drift the mean a small fraction of target_sd; occasional outlier shifts it 2-3 SD
	driftSD := 0.3
	if outlier {
		driftSD = 2.0 + rand.Float64()
		if rand.Float64() < 0.5 {
			driftSD = -driftSD
		}
	}
	mean := c.targetMean + c.targetSD*driftSD*(rand.Float64()*2-1)
	if outlier {
		mean = c.targetMean + c.targetSD*driftSD
	}

	// day SD wobbles around target_sd by +/- 30%
	sd := c.targetSD * (0.85 + rand.Float64()*0.3)

	return c.n, mean, sd
}

// upsertLabControlDay computes ±1/2 SD bands from the day's own mean and sd.
func upsertLabControlDay(db *sql.DB, controlID string, day time.Time, n int, mean, sd float64) error {
	m1n := mean - sd
	m2n := mean - 2*sd
	p1 := mean + sd
	p2 := mean + 2*sd

	// keep the bands sane: round to 4 decimals to avoid float noise in display
	round4 := func(v float64) float64 { return math.Round(v*10000) / 10000 }

	_, err := db.Exec(`
		INSERT INTO lab_control_daily (control_id, day, n, mean, sd, minus_2sd, minus_1sd, plus_1sd, plus_2sd)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (control_id, day) DO UPDATE SET
			n         = EXCLUDED.n,
			mean      = EXCLUDED.mean,
			sd        = EXCLUDED.sd,
			minus_2sd = EXCLUDED.minus_2sd,
			minus_1sd = EXCLUDED.minus_1sd,
			plus_1sd  = EXCLUDED.plus_1sd,
			plus_2sd  = EXCLUDED.plus_2sd
	`, controlID, day, n, round4(mean), round4(sd), round4(m2n), round4(m1n), round4(p1), round4(p2))
	return err
}

// seedLabControls drops and reseeds lab control tables with the configured controls
// and the last `daysBack` days (excluding today) of aggregated daily values.
func seedLabControls(db *sql.DB, daysBack int) error {
	log.Println("Seeding lab controls (drop + reseed)...")

	if _, err := db.Exec("TRUNCATE TABLE lab_control_daily, lab_controls RESTART IDENTITY CASCADE"); err != nil {
		return fmt.Errorf("truncate lab control tables: %w", err)
	}

	for _, c := range labControls {
		if _, err := db.Exec(`
			INSERT INTO lab_controls (control_id, name, analyte, unit, target_mean, target_sd)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, c.id, c.name, c.analyte, c.unit, c.targetMean, c.targetSD); err != nil {
			return fmt.Errorf("insert control %s: %w", c.id, err)
		}
	}

	// historical days: from (today - daysBack) up to (today - 1 day), inclusive
	today := time.Now().UTC().Truncate(24 * time.Hour)
	startDay := today.AddDate(0, 0, -daysBack)
	rowCount := 0

	for d := startDay; d.Before(today); d = d.AddDate(0, 0, 1) {
		for _, c := range labControls {
			n, mean, sd := generateDailyAggregate(c, d)
			if err := upsertLabControlDay(db, c.id, d, n, mean, sd); err != nil {
				return fmt.Errorf("upsert daily %s %s: %w", c.id, d.Format("2006-01-02"), err)
			}
			rowCount++
		}
	}

	log.Printf("Seeded %d lab controls with %d daily aggregate rows (%d days history)", len(labControls), rowCount, daysBack)
	db.Exec("ANALYZE lab_controls")
	db.Exec("ANALYZE lab_control_daily")
	return nil
}
