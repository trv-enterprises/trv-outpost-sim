#!/usr/bin/env python3
"""
Traffic-sim data-shape spike.

Reads the AWS Honeypot ("marx-geo") canonical CSV, aggregates it two ways, and
emits real ECharts configs for the two target Outpost views:

  - globe_config.json : echarts-gl `globe` + `lines3D` weighted arcs
  - sankey_config.json: echarts `sankey` country -> host flows

Also emits a few sample emitted-records (the shape the Go simulator will stream)
so the record schema in TRAFFIC-SIM-PLAN.md can be locked.

Input CSV is NOT committed (51MB). Default path: /tmp/marx-geo.csv
  curl -sL https://raw.githubusercontent.com/tcrug/marx_data/master/marx-geo.csv.gz \
    | gunzip > /tmp/marx-geo.csv
"""
import csv
import json
import math
import os
from collections import Counter, defaultdict

CSV_PATH = os.environ.get("MARX_CSV", "/tmp/marx-geo.csv")
OUT_DIR = os.path.dirname(os.path.abspath(__file__))

# host (honeypot) -> AWS region destination coords. Geocoded by hand, once.
# Names map to AWS regions; coords are the region's city anchor.
HOST_DEST = {
    "groucho-tokyo":     {"region": "Tokyo (ap-northeast-1)",     "lat": 35.68,  "lon": 139.76},
    "groucho-oregon":    {"region": "Oregon (us-west-2)",         "lat": 45.84,  "lon": -119.70},
    "groucho-singapore": {"region": "Singapore (ap-southeast-1)", "lat": 1.29,   "lon": 103.85},
    "groucho-us-east":   {"region": "N. Virginia (us-east-1)",    "lat": 38.95,  "lon": -77.45},
    "groucho-norcal":    {"region": "N. California (us-west-1)",  "lat": 37.78,  "lon": -122.42},
    "zeppo-norcal":      {"region": "N. California (us-west-1)",  "lat": 37.78,  "lon": -122.42},
    "groucho-sydney":    {"region": "Sydney (ap-southeast-2)",    "lat": -33.87, "lon": 151.21},
    "groucho-sa":        {"region": "Sao Paulo (sa-east-1)",      "lat": -23.55, "lon": -46.63},
    "groucho-eu":        {"region": "Ireland (eu-west-1)",        "lat": 53.41,  "lon": -8.24},
}

TOP_N_COUNTRIES = 15  # for the sankey + globe; the long tail is noise


def parse_float(s):
    try:
        return float(s)
    except (ValueError, TypeError):
        return None


def main():
    # Aggregations
    country_host = Counter()                 # (country, host) -> count   (sankey links)
    country_coord = {}                        # country -> (lat, lon) representative source point
    country_coord_votes = defaultdict(Counter)  # country -> Counter[(lat,lon)] to pick modal coord
    host_count = Counter()
    proto_count = Counter()
    total = 0
    dropped_nocoord = 0
    dropped_nohost = 0
    sample_records = []

    with open(CSV_PATH, newline="") as f:
        reader = csv.DictReader(f)
        for row in reader:
            total += 1
            host = row["host"]
            if host not in HOST_DEST:
                dropped_nohost += 1
                continue
            lat = parse_float(row["latitude"])
            lon = parse_float(row["longitude"])
            if lat is None or lon is None or (lat == 0 and lon == 0):
                dropped_nocoord += 1
                continue
            country = row["country"] or row["cc"] or "Unknown"

            country_host[(country, host)] += 1
            host_count[host] += 1
            proto_count[row["proto"]] += 1
            country_coord_votes[country][(lat, lon)] += 1

            if len(sample_records) < 5:
                sample_records.append({
                    "timestamp": row["datetime"],
                    "src": {
                        "ip": row["srcstr"],
                        "country": country,
                        "cc": row["cc"],
                        "city": row["locale"] or None,
                        "lat": lat,
                        "lon": lon,
                    },
                    "dst": {
                        "host": host,
                        "region": HOST_DEST[host]["region"],
                        "lat": HOST_DEST[host]["lat"],
                        "lon": HOST_DEST[host]["lon"],
                    },
                    "proto": row["proto"],
                    "attack_type": row["type"] or None,
                    "spt": int(row["spt"]) if row["spt"] else None,
                    "dpt": int(row["dpt"]) if row["dpt"] else None,
                    "value": 1,
                })

    # pick modal coord per country (the most-attacked city = good arc origin)
    for country, votes in country_coord_votes.items():
        country_coord[country] = votes.most_common(1)[0][0]

    # Top-N countries by total volume
    country_total = Counter()
    for (country, host), c in country_host.items():
        country_total[country] += c
    top_countries = [c for c, _ in country_total.most_common(TOP_N_COUNTRIES)]
    top_set = set(top_countries)

    kept = sum(country_host.values())

    # ---- Build globe (echarts-gl) config: weighted arcs top-country -> host ----
    # lines3D wants coords [[srcLon,srcLat],[dstLon,dstLat]]; weight -> log width.
    arc_data = []
    max_c = max(c for (country, host), c in country_host.items() if country in top_set)
    for (country, host), c in country_host.items():
        if country not in top_set:
            continue
        slat, slon = country_coord[country]
        d = HOST_DEST[host]
        # log-scaled width so China doesn't dwarf everything
        w = 0.5 + 4.0 * (math.log1p(c) / math.log1p(max_c))
        arc_data.append({
            "coords": [[slon, slat], [d["lon"], d["lat"]]],
            "value": c,
            "lineStyle": {"width": round(w, 2)},
            "country": country,
            "host": host,
        })
    arc_data.sort(key=lambda a: a["value"], reverse=True)

    globe_config = {
        "backgroundColor": "#000",
        "globe": {
            "baseTexture": "",  # dashboard supplies earth texture asset
            "shading": "color",
            "atmosphere": {"show": True},
            "viewControl": {"autoRotate": True, "autoRotateSpeed": 4},
        },
        "series": [{
            "type": "lines3D",
            "coordinateSystem": "globe",
            "blendMode": "lighter",
            "effect": {"show": True, "trailWidth": 2, "trailLength": 0.2,
                       "trailOpacity": 1, "trailColor": "#fb7293"},
            "lineStyle": {"width": 1, "color": "#fb7293", "opacity": 0.1},
            "data": arc_data,
        }],
    }

    # ---- Build sankey config: country -> host weighted flows ----
    # Region nodes must be UNIQUE by name. Note groucho-norcal + zeppo-norcal
    # both map to "N. California (us-west-1)", so dedupe by region and merge
    # their links — echarts Sankey crashes on duplicate node names.
    nodes = [{"name": c} for c in top_countries]
    region_names = sorted({HOST_DEST[h]["region"] for (_, h), _ in country_host.items()})
    nodes += [{"name": r} for r in region_names]

    link_weights = Counter()  # (country, region) -> summed count
    for (country, host), c in country_host.items():
        if country not in top_set:
            continue
        link_weights[(country, HOST_DEST[host]["region"])] += c
    links = [
        {"source": country, "target": region, "value": c}
        for (country, region), c in link_weights.items()
    ]
    links.sort(key=lambda l: l["value"], reverse=True)

    sankey_config = {
        "series": [{
            "type": "sankey",
            "emphasis": {"focus": "adjacency"},
            "nodeAlign": "left",
            "lineStyle": {"color": "gradient", "curveness": 0.5},
            "data": nodes,
            "links": links,
        }],
    }

    # ---- Write outputs ----
    with open(os.path.join(OUT_DIR, "globe_config.json"), "w") as f:
        json.dump(globe_config, f, indent=2)
    with open(os.path.join(OUT_DIR, "sankey_config.json"), "w") as f:
        json.dump(sankey_config, f, indent=2)
    with open(os.path.join(OUT_DIR, "sample_records.json"), "w") as f:
        json.dump(sample_records, f, indent=2)

    # ---- Report ----
    print(f"total rows         : {total}")
    print(f"kept (aggregated)  : {kept}")
    print(f"dropped no-coord   : {dropped_nocoord}")
    print(f"dropped no-host    : {dropped_nohost}")
    print(f"distinct hosts     : {len(host_count)}")
    print(f"distinct countries : {len(country_total)}")
    print(f"top-{TOP_N_COUNTRIES} countries  : {', '.join(top_countries)}")
    print(f"globe arcs         : {len(arc_data)} (top-N countries x hosts)")
    print(f"sankey nodes/links : {len(nodes)} / {len(links)}")
    print(f"protocols          : {dict(proto_count)}")
    print("wrote: globe_config.json, sankey_config.json, sample_records.json")


if __name__ == "__main__":
    main()
