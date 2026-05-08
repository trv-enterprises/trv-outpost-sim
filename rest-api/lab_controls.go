package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

type LabControl struct {
	ControlID  string  `json:"control_id"`
	Name       string  `json:"name"`
	Analyte    string  `json:"analyte"`
	Unit       string  `json:"unit"`
	TargetMean float64 `json:"target_mean"`
	TargetSD   float64 `json:"target_sd"`
	IsActive   bool    `json:"is_active"`
}

type LabControlDaily struct {
	ControlID string  `json:"control_id"`
	Day       string  `json:"day"` // YYYY-MM-DD
	N         int     `json:"n"`
	Mean      float64 `json:"mean"`
	SD        float64 `json:"sd"`
	Minus2SD  float64 `json:"minus_2sd"`
	Minus1SD  float64 `json:"minus_1sd"`
	Plus1SD   float64 `json:"plus_1sd"`
	Plus2SD   float64 `json:"plus_2sd"`
}

var labControlsDB *sql.DB

// initLabControlsDB opens the postgres connection. Returns nil on failure so
// the rest-api still starts (lab-control endpoints will return 503).
func initLabControlsDB(dsn string) {
	if dsn == "" {
		log.Printf("Lab-control endpoints: disabled (no POSTGRES_DSN)")
		return
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("Lab-control endpoints: open db failed: %v", err)
		return
	}
	if err := db.Ping(); err != nil {
		log.Printf("Lab-control endpoints: ping db failed: %v", err)
		return
	}
	labControlsDB = db
	log.Printf("Lab-control endpoints: enabled")
}

func registerLabControlRoutes(r *mux.Router) {
	r.HandleFunc("/api/lab-controls", getLabControls).Methods("GET", "OPTIONS")
	r.HandleFunc("/api/lab-controls/{id}/daily", getLabControlDaily).Methods("GET", "OPTIONS")
}

func getLabControls(w http.ResponseWriter, r *http.Request) {
	if labControlsDB == nil {
		http.Error(w, "lab-control endpoints unavailable", http.StatusServiceUnavailable)
		return
	}
	rows, err := labControlsDB.Query(`
		SELECT control_id, name, analyte, unit, target_mean, target_sd, is_active
		FROM lab_controls
		ORDER BY control_id
	`)
	if err != nil {
		http.Error(w, fmt.Sprintf("query: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []LabControl{}
	for rows.Next() {
		var c LabControl
		if err := rows.Scan(&c.ControlID, &c.Name, &c.Analyte, &c.Unit, &c.TargetMean, &c.TargetSD, &c.IsActive); err != nil {
			http.Error(w, fmt.Sprintf("scan: %v", err), http.StatusInternalServerError)
			return
		}
		out = append(out, c)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func getLabControlDaily(w http.ResponseWriter, r *http.Request) {
	if labControlsDB == nil {
		http.Error(w, "lab-control endpoints unavailable", http.StatusServiceUnavailable)
		return
	}
	id := mux.Vars(r)["id"]
	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= 365 {
			days = v
		}
	}

	cutoff := time.Now().UTC().Truncate(24 * time.Hour).AddDate(0, 0, -days)
	rows, err := labControlsDB.Query(`
		SELECT control_id, day, n, mean, sd, minus_2sd, minus_1sd, plus_1sd, plus_2sd
		FROM lab_control_daily
		WHERE control_id = $1 AND day >= $2
		ORDER BY day ASC
	`, id, cutoff)
	if err != nil {
		http.Error(w, fmt.Sprintf("query: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []LabControlDaily{}
	for rows.Next() {
		var d LabControlDaily
		var day time.Time
		if err := rows.Scan(&d.ControlID, &day, &d.N, &d.Mean, &d.SD, &d.Minus2SD, &d.Minus1SD, &d.Plus1SD, &d.Plus2SD); err != nil {
			http.Error(w, fmt.Sprintf("scan: %v", err), http.StatusInternalServerError)
			return
		}
		d.Day = day.Format("2006-01-02")
		out = append(out, d)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
