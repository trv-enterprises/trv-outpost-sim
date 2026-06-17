# Traffic-sim data-shape spike

Proves the AWS Honeypot ("marx-geo") dataset renders well on both target views
**before** building the Go simulator. See `../TRAFFIC-SIM-PLAN.md` for context.

## What's here

- `build_spike.py` — reads the canonical CSV, aggregates, emits ECharts configs.
- `globe_config.json` — `echarts-gl` `globe` + `lines3D` weighted arcs.
- `sankey_config.json` — `sankey` country → honeypot-region flows.
- `sample_records.json` — 5 records in the shape the Go sim will emit (schema lock).
- `preview.html` — loads echarts + echarts-gl from CDN and renders both configs.

## Reproduce

The 51 MB source CSV is **not committed**. Fetch it (canonical mirror, no login):

```sh
curl -sL https://raw.githubusercontent.com/tcrug/marx_data/master/marx-geo.csv.gz \
  | gunzip > /tmp/marx-geo.csv
python3 build_spike.py            # reads /tmp/marx-geo.csv by default
# or: MARX_CSV=/path/to/marx-geo.csv python3 build_spike.py
```

Then open `preview.html` (any static server; it fetches the JSON siblings):

```sh
python3 -m http.server 8099   # then visit http://localhost:8099/preview.html
```

## Verified facts (from the real 451,581-row file)

- **Canonical 15-col schema** confirmed (`datetime,host,src,proto,type,spt,dpt,srcstr,cc,country,locale,localeabbr,postalcode,latitude,longitude`) — NOT the altered capitalone copy.
- **9 honeypot hosts** (plan assumed ~5): tokyo, oregon, singapore, us-east,
  groucho-norcal, zeppo-norcal, sydney, sa (São Paulo), eu (Ireland). All map to
  AWS regions; geocoded in `HOST_DEST` in `build_spike.py`.
- Source coords are real city-level lat/lon, **baked in** (no GeoIP step). Only
  **3,428 rows (0.76%)** lack coords → dropped.
- Top source countries: China (191k), United States (90k), Japan, Iran, Taiwan…
- Protocols: TCP 327k / UDP 77k / ICMP 44k.
- No count column — each row = 1 event; weights come from `GROUP BY country, host`.
