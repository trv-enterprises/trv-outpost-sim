package main

import (
	"compress/gzip"
	"embed"
	"encoding/csv"
	"io"
	"log"
	"sort"
	"strconv"
)

// The honeypot dataset is bundled gzipped (~7MB vs ~51MB raw) and decompressed
// at startup. Canonical "marx-geo" AWS Honeypot data; see docs/spike/README.md.
//
//go:embed data/marx-geo.csv.gz
var embeddedData embed.FS

// region is a honeypot destination (where the attack landed). The dataset's
// `host` column maps to an AWS region we geocode by hand — see hostDest.
type region struct {
	Region string  `json:"region"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

// hostDest maps each honeypot `host` to its AWS-region destination coords.
// Mirrors HOST_DEST in docs/spike/build_spike.py. NOTE: groucho-norcal and
// zeppo-norcal intentionally share the "N. California (us-west-1)" region.
var hostDest = map[string]region{
	"groucho-tokyo":     {"Tokyo (ap-northeast-1)", 35.68, 139.76},
	"groucho-oregon":    {"Oregon (us-west-2)", 45.84, -119.70},
	"groucho-singapore": {"Singapore (ap-southeast-1)", 1.29, 103.85},
	"groucho-us-east":   {"N. Virginia (us-east-1)", 38.95, -77.45},
	"groucho-norcal":    {"N. California (us-west-1)", 37.78, -122.42},
	"zeppo-norcal":      {"N. California (us-west-1)", 37.78, -122.42},
	"groucho-sydney":    {"Sydney (ap-southeast-2)", -33.87, 151.21},
	"groucho-sa":        {"Sao Paulo (sa-east-1)", -23.55, -46.63},
	"groucho-eu":        {"Ireland (eu-west-1)", 53.41, -8.24},
}

// src is the attacker endpoint (geo baked into the dataset; no GeoIP step).
type src struct {
	IP      string  `json:"ip"`
	Country string  `json:"country"`
	CC      string  `json:"cc"`
	City    string  `json:"city,omitempty"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

// Event is the locked emitted-record schema (matches docs/spike/sample_records.json).
// Timestamp is rewritten to ~now on each replay pass so the stream never runs dry.
type Event struct {
	Timestamp  int64  `json:"timestamp"`
	Src        src    `json:"src"`
	Dst        region `json:"dst"`
	Proto      string `json:"proto"`
	AttackType string `json:"attack_type,omitempty"`
	Spt        int    `json:"spt"`
	Dpt        int    `json:"dpt"`
	Value      int    `json:"value"`
}

// Flow is one aggregated edge for the static globe arcs / Sankey links.
type Flow struct {
	Country string  `json:"country"`
	CC      string  `json:"cc"`
	SrcLat  float64 `json:"src_lat"`
	SrcLon  float64 `json:"src_lon"`
	Region  string  `json:"region"`
	DstLat  float64 `json:"dst_lat"`
	DstLon  float64 `json:"dst_lon"`
	Count   int     `json:"count"`
}

// loadEvents decompresses and parses the embedded CSV into events. Rows missing
// coordinates or with an unknown host are dropped (mirrors the spike's filter).
func loadEvents() []Event {
	f, err := embeddedData.Open("data/marx-geo.csv.gz")
	if err != nil {
		log.Fatalf("open embedded data: %v", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		log.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()

	r := csv.NewReader(gz)
	r.ReuseRecord = true
	r.FieldsPerRecord = -1 // tolerate stray short rows

	// header: datetime,host,src,proto,type,spt,dpt,srcstr,cc,country,locale,localeabbr,postalcode,latitude,longitude
	const (
		cHost    = 1
		cProto   = 3
		cType    = 4
		cSpt     = 5
		cDpt     = 6
		cSrcstr  = 7
		cCC      = 8
		cCountry = 9
		cLocale  = 10
		cLat     = 13
		cLon     = 14
		nCols    = 15
	)

	if _, err := r.Read(); err != nil { // discard header
		log.Fatalf("read header: %v", err)
	}

	events := make([]Event, 0, 450000)
	var dropped int
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("csv read error (skipping row): %v", err)
			continue
		}
		if len(rec) < nCols {
			dropped++
			continue
		}

		dst, ok := hostDest[rec[cHost]]
		if !ok {
			dropped++
			continue
		}
		lat, errLat := strconv.ParseFloat(rec[cLat], 64)
		lon, errLon := strconv.ParseFloat(rec[cLon], 64)
		if errLat != nil || errLon != nil || (lat == 0 && lon == 0) {
			dropped++
			continue
		}

		country := rec[cCountry]
		if country == "" {
			country = rec[cCC]
		}
		spt, _ := strconv.Atoi(rec[cSpt])
		dpt, _ := strconv.Atoi(rec[cDpt])

		events = append(events, Event{
			Src: src{
				IP:      rec[cSrcstr],
				Country: country,
				CC:      rec[cCC],
				City:    rec[cLocale],
				Lat:     lat,
				Lon:     lon,
			},
			Dst:        dst,
			Proto:      rec[cProto],
			AttackType: rec[cType],
			Spt:        spt,
			Dpt:        dpt,
			Value:      1,
		})
	}

	log.Printf("loaded %d events (%d rows dropped: no-coord/unknown-host/short)", len(events), dropped)
	return events
}

// aggregate collapses events into per-(country,region) flows. Region is unique
// by name, so the two norcal hosts merge into one flow per country — this is the
// invariant the echarts Sankey requires (no duplicate node names).
func aggregate(events []Event) []Flow {
	type key struct{ country, region string }
	acc := make(map[key]*Flow)
	for i := range events {
		e := &events[i]
		k := key{e.Src.Country, e.Dst.Region}
		f := acc[k]
		if f == nil {
			f = &Flow{
				Country: e.Src.Country,
				CC:      e.Src.CC,
				SrcLat:  e.Src.Lat,
				SrcLon:  e.Src.Lon,
				Region:  e.Dst.Region,
				DstLat:  e.Dst.Lat,
				DstLon:  e.Dst.Lon,
			}
			acc[k] = f
		}
		f.Count++
	}

	flows := make([]Flow, 0, len(acc))
	for _, f := range acc {
		flows = append(flows, *f)
	}
	sort.Slice(flows, func(i, j int) bool { return flows[i].Count > flows[j].Count })
	return flows
}
