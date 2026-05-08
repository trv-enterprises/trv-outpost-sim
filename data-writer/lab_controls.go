package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"math/rand"
	"time"

	_ "github.com/lib/pq"
)

type labControl struct {
	id         string
	targetMean float64
	targetSD   float64
	n          int
}

// readActiveControls loads control catalog rows seeded by db-seed. The seeder
// is the source of truth for control definitions; the writer just appends
// daily aggregates.
func readActiveControls(db *sql.DB) ([]labControl, error) {
	rows, err := db.Query(`
		SELECT control_id, target_mean, target_sd
		FROM lab_controls
		WHERE is_active = true
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []labControl{}
	for rows.Next() {
		var c labControl
		if err := rows.Scan(&c.id, &c.targetMean, &c.targetSD); err != nil {
			return nil, err
		}
		c.n = 6
		out = append(out, c)
	}
	return out, rows.Err()
}

func generateLabControlDaily(c labControl) (int, float64, float64) {
	outlier := rand.Float64() < 0.05
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
	sd := c.targetSD * (0.85 + rand.Float64()*0.3)
	return c.n, mean, sd
}

func upsertLabControlDay(db *sql.DB, controlID string, day time.Time, n int, mean, sd float64) error {
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
	`, controlID, day, n,
		round4(mean), round4(sd),
		round4(mean-2*sd), round4(mean-sd),
		round4(mean+sd), round4(mean+2*sd))
	return err
}

// gapFillThroughYesterday inserts a daily aggregate for any missing day from the
// earliest existing date through yesterday (UTC). Run on writer startup.
func gapFillThroughYesterday(db *sql.DB, controls []labControl) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)

	for _, c := range controls {
		var maxDay sql.NullTime
		if err := db.QueryRow(`SELECT MAX(day) FROM lab_control_daily WHERE control_id = $1`, c.id).Scan(&maxDay); err != nil {
			return fmt.Errorf("max day for %s: %w", c.id, err)
		}
		if !maxDay.Valid {
			continue // seeder hasn't run; nothing to gap-fill against
		}

		next := maxDay.Time.UTC().AddDate(0, 0, 1)
		for d := next; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
			n, mean, sd := generateLabControlDaily(c)
			if err := upsertLabControlDay(db, c.id, d, n, mean, sd); err != nil {
				return fmt.Errorf("gap-fill %s %s: %w", c.id, d.Format("2006-01-02"), err)
			}
			log.Printf("lab-control: gap-filled %s for %s", c.id, d.Format("2006-01-02"))
		}
	}
	return nil
}

// runLabControlDailyLoop fires once at boot (gap-fill) and then once per day,
// writing a row dated for the previous calendar day (UTC).
func runLabControlDailyLoop(dsn string) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("lab-control: open db failed: %v (loop disabled)", err)
		return
	}
	if err := db.Ping(); err != nil {
		log.Printf("lab-control: ping db failed: %v (loop disabled)", err)
		return
	}

	// The seeder may still be creating tables/rows when this loop starts.
	// Retry until the catalog is reachable, with backoff up to ~5 minutes total.
	var controls []labControl
	for attempt := 0; attempt < 30; attempt++ {
		var err error
		controls, err = readActiveControls(db)
		if err == nil {
			break
		}
		log.Printf("lab-control: catalog not ready (attempt %d): %v", attempt+1, err)
		time.Sleep(10 * time.Second)
	}
	if controls == nil {
		log.Printf("lab-control: catalog never became reachable; loop disabled")
		return
	}
	if len(controls) == 0 {
		log.Printf("lab-control: no active controls in DB; loop will idle until tomorrow")
	}

	if err := gapFillThroughYesterday(db, controls); err != nil {
		log.Printf("lab-control: gap-fill error: %v", err)
	}

	for {
		now := time.Now().UTC()
		nextMidnight := now.Truncate(24 * time.Hour).AddDate(0, 0, 1)
		// fire 1 minute past midnight so any clock skew settles
		fireAt := nextMidnight.Add(time.Minute)
		time.Sleep(time.Until(fireAt))

		// reload controls in case the catalog changed
		controls, err = readActiveControls(db)
		if err != nil {
			log.Printf("lab-control: reload controls failed: %v", err)
			continue
		}

		yesterday := time.Now().UTC().Truncate(24 * time.Hour).AddDate(0, 0, -1)
		for _, c := range controls {
			n, mean, sd := generateLabControlDaily(c)
			if err := upsertLabControlDay(db, c.id, yesterday, n, mean, sd); err != nil {
				log.Printf("lab-control: write %s %s failed: %v", c.id, yesterday.Format("2006-01-02"), err)
				continue
			}
			log.Printf("lab-control: wrote daily aggregate %s for %s", c.id, yesterday.Format("2006-01-02"))
		}
	}
}
