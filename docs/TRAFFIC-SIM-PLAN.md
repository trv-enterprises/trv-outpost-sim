# Traffic Simulator + Globe/Sankey — Plan & Handoff

> **Status:** spike DONE (2026-06-16), decisions locked, ready to build the Go
> service. This doc is a handoff so a fresh Claude Code session has full context.

## Spike outcome (2026-06-16) — see `docs/spike/`

Ran the data-shape spike against the real 451,581-row canonical CSV. Verdict:
**shape works on both views, green light to build.** Artifacts in `docs/spike/`
(`build_spike.py`, `globe_config.json`, `sankey_config.json`,
`sample_records.json`, `preview.html`, `globe_preview.png`). The 51MB CSV is
gitignored; `docs/spike/README.md` has the one-line fetch.

Key findings that update this plan:
- ✅ Canonical 15-col schema confirmed via mirror `tcrug/marx_data/marx-geo.csv.gz`
  (NOT the altered capitalone copy).
- ⚠️ **9 honeypot hosts, not ~5**: tokyo, oregon, singapore, us-east,
  groucho-norcal, zeppo-norcal, sydney, sa (São Paulo), eu (Ireland). All map to
  AWS regions; lookup table is `HOST_DEST` in `build_spike.py` — reuse it.
- Only 3,428 rows (0.76%) lack coords → drop them. 448,153 aggregated.
- Globe rendered great (log-scaled arc widths, flying-line trails). Sankey config
  validated (23 nodes / 135 links, no dangling endpoints).
- Emitted record schema confirmed, with `dst.region` added (human label).

## Decisions locked (Tom, 2026-06-16)

- **Transport:** WebSocket stream **AND** ts-store schema store (option 1).
- **WS must not run dry:** loop the dataset continuously and **rewrite each
  event's timestamp to ~now on every pass** (round-robin replay with fresh
  timestamps). This is the explicit fix for "the stream runs dry."
- **Cadence:** accelerated continuous loop (compress the data into a fast loop).
- **Globe:** BOTH modes — static weighted arcs (from ts-store aggregate) as a
  base layer + live flying arcs (from the WS event stream).

## Decisions locked (Tom, 2026-06-17) — deployment shape

- **Dataset packaging:** **bake the gzipped CSV into the image.** Commit a
  gzipped copy (or representative subset) into this repo (`data/`), un-ignore
  just that file, and decompress at Docker build. Self-contained, reproducible,
  no network at deploy time. The plain 51MB CSV stays gitignored.
- **ts-store store creation:** **role-managed schema store** (NOT self-create at
  startup). The new traffic schema store is declared in the trv-homelab Ansible
  role like `sensor-readings`, and the role creates it + applies the schema +
  reconciles the shared key before the writer starts. ts-store does NOT
  auto-create stores, so this is required.

### ⚠️ Coupled change — sim repo + trv-homelab role must land together

The Go writer's emitted field set MUST exactly match the schema the role
declares. A schema store rejects any field not declared (`field not found in
schema: <field>`). So:

- The schema is defined ONCE as the **single source of truth** in
  `traffic-writer/schema.go` (`trafficFlowSchema`, a `{index,name,type}` list)
  and exported verbatim as **`traffic-writer/schema.json`** (also served live at
  `GET /schema`). The deploy session copies that field list into
  `simulators_tsstore_schema_stores` in
  `trv-homelab/tools/ansible/roles/simulators/vars/main.yml` — do NOT re-type it
  by hand; copy from `schema.json`. The writer calls `validateSchema()` at
  startup, so the emitted record and the declared schema cannot silently drift.

### Build punch list (this repo) — ✅ DONE (2026-06-17)

All in `traffic-writer/`. Built, `go vet`-clean, integration-tested end-to-end
(WS stream with fresh timestamps verified; ts-store writer verified against a
mock: 1038 flows/cycle, 0 errors). Docker image builds.

1. ✅ `traffic-writer/` Go service — embeds `data/marx-geo.csv.gz`, decompresses
   in-memory, round-robin replay with timestamp→now each pass. Dual output:
   WS stream (`/ws`) + ts-store schema-store writes. Reuses `HOST_DEST` (9 hosts)
   and the locked emitted-record schema. Also serves `/aggregate` (static
   weighted flows for the globe base layer + Sankey) and `/schema`.
2. ✅ `traffic-writer/Dockerfile` — mirrors `data-writer/Dockerfile`; CSV is
   `go:embed`'d (decompressed at runtime, not build).
3. ✅ Gzipped dataset committed at `traffic-writer/data/marx-geo.csv.gz` (7.2MB).
   The plain 51MB CSV stays gitignored (`docs/spike/.gitignore`).
4. ✅ `docker-compose.yml` — `traffic-writer` service: build context, port
   **21084:8085**, `depends_on: tsstore` (healthy), `TSSTORE_URL` +
   shared `TSSTORE_API_KEY`. Env defaults added to `.env.example`.

**Store name:** `traffic-flows`. **Host port:** `21084` (WS+HTTP). **Env vars:**
`TRAFFIC_EVENTS_PER_SEC` (default 50), `TRAFFIC_STORE_NAME`, `TRAFFIC_AGG_WRITE_SEC`.

### Handoff to trv-homelab / homelab-deploy (the deploy session does these)

5. trv-homelab: add the traffic schema-store spec to `roles/simulators/vars/main.yml`
   — **copy the field list from `traffic-writer/schema.json`** (store
   `traffic-flows`, `data_type: schema`, the 9 `{index,name,type}` fields).
   Thread port **21084** through `defaults/main.yml` + `templates/simulators.env.j2`.
6. homelab-deploy: update CLAUDE.md services/ports table (add traffic-writer @
   21084); `make deploy-simulators`; verify `failed=0`, `traffic-flows` store
   created, writer healthy, WS emitting, ts-store 0.8.3.

> The globe + Sankey dashboard components are a SEPARATE track in the dashboard
> repo (`trv-outpost`), built from the spike configs after the sim feed is live.


## Goal

Add a **network-traffic simulator** to this repo that **replays a real dataset**
of traffic between geo-located endpoints, to feed two visualizations in the
**Outpost dashboard**:

1. **3D globe** — arcs between source/dest coordinates, rendered with
   **`echarts-gl`** (`globe` + `lines3D`/flying-lines). A custom dashboard
   component loaded via the dashboard's `DynamicComponentLoader`.
   `echarts-gl` is **already a dependency** of the dashboard client — no new dep.
2. **Sankey** — same data as flows between nodes (source → dest, link width = volume).

One dataset feeds **both** views. The shared record shape is:

```
source { lat, lon, ip? }  →  target { lat, lon, ip? }  +  value (volume/count)
```

- Globe uses the coordinate pairs (`lines3D` wants `[[srcLon,srcLat],[dstLon,dstLat]]`).
- Sankey uses the endpoints as nodes and `value` as link width.
- IP on each end is a bonus: good Sankey node labels + ties a flow to an endpoint.

## Dataset (DECIDED): AWS Honeypot Attack Data

Chosen after a research sweep (honeypot logs are the only public data with
real IP + geo on the source + a target + volume; CAIDA/MAWI anonymize IPs and
have no geo, and IDS sets like CIC-IDS/UNSW-NB15 use private/lab IPs that
geocode to nowhere).

- **Source:** Kaggle — https://www.kaggle.com/datasets/casimian2000/aws-honeypot-attack-data
- **No-login mirror to try first:** the underlying "Marx" CSV via `tcrug/marx_data` on GitHub.
  - **⚠️ Do NOT use the `capitalone/DataProfiler` copy** — verified to be an
    *altered test fixture* (renamed `srcip`/`srcport` cols + synthetic
    `owner`/`comment`/`int_col` filler), NOT the canonical file.
- **Canonical schema (15 columns):**
  `datetime, host, src, proto, type, spt, dpt, srcstr, cc, country, locale, localeabbr, postalcode, latitude, longitude`
  - `srcstr` = **real attacker IP** (dotted quad, not anonymized); `src` = int form
  - `latitude` / `longitude` = **source coords, already baked in** (no GeoIP step!)
  - `cc` / `country` / `locale` = attacker country/city
  - `host` = which honeypot was hit (`groucho-oregon`, `groucho-singapore`,
    `groucho-tokyo`, `groucho-us-east`, `zeppo-norcal`) → the **destination**;
    geocode these ~5 region names ONCE, by hand (small lookup table).
  - `spt` / `dpt` = source/dest port; `proto`, `type` = protocol/attack type
  - **No count column** — each of ~451k rows is one attack event. Aggregate
    (`GROUP BY srcstr` / `country` / `host`) to get link weights/arc intensity.
- **License:** ⚠️ Kaggle page is JS-gated; license tag wasn't machine-verifiable.
  **Confirm the license before redistributing** in any non-personal context.
  For a personal homelab demo it's almost certainly fine.

**Runner-up if license is a problem:** Hornet 40 (Stratosphere) — open download,
real per-flow bytes/packets, 8 known dest cities, but you geocode source IPs
with MaxMind GeoLite2, and it's **CC BY-NC-ND** (non-commercial, no-derivatives).

## The spike to do FIRST (before building the full service)

Low-effort, high-signal — prove the data renders the way we want before
plumbing a new Go simulator service:

1. **Fetch a sample** of the canonical honeypot CSV (mirror first; Kaggle if needed).
   Verify it's the 15-column canonical schema, not the altered capitalone copy.
2. **Lock the record schema** the simulator will emit (proposed below).
3. **Build the destination lookup** — map the ~5 `host` honeypot names to lat/lon.
4. **Produce two real echarts configs** from actual aggregated rows:
   - a `globe` + `lines3D` config (arcs, weighted),
   - a `sankey` config (country → host, weighted).
   Drop them in `docs/spike/` as JSON (or a tiny HTML harness) so they can be
   eyeballed and pasted into a dashboard dynamic component.
5. Report: does the shape look good on both? Any aggregation tuning needed
   (top-N countries, log-scale arc width, etc.)?

### Proposed emitted record (simulator → consumers)

```json
{
  "timestamp": 1699999999999,
  "src": { "ip": "1.2.3.4", "country": "CN", "city": "...", "lat": 31.2, "lon": 121.5 },
  "dst": { "host": "groucho-singapore", "lat": 1.29, "lon": 103.85 },
  "proto": "TCP", "attack_type": "...", "spt": 51234, "dpt": 22,
  "value": 1
}
```
Aggregated view for Sankey/arc weight: `GROUP BY src.country, dst.host → sum(value)`.

## Where things live (repo boundaries — IMPORTANT)

- **This repo (`trv-outpost-sim`)** — the traffic simulator service (Go, same
  pattern as the existing `data-writer` / `websocket` / `rest-api` sims). Add a
  new service + compose entry here.
- **Dashboard repo (`trv-enterprises/trv-outpost`)** — the globe + Sankey custom
  components (React, loaded via `DynamicComponentLoader`). `echarts-gl` already
  present there.
- **`trv-homelab`** — Ansible role syncs THIS repo from `../trv-outpost-sim`.
  Deploy via `make deploy-simulators` from `homelab-deploy`. ts-store is pinned
  to `0.8.3` in `homelab-deploy/inventory/host_vars/simulators.yml`.

## Existing simulator pattern to follow

Look at `data-writer/`, `websocket/`, `rest-api/` in this repo. Each is a small
Go service with a `Dockerfile`, wired into `docker-compose.yml`, writing to
ts-store and/or serving an endpoint. The new traffic sim should mirror this:
a Go service that loads the honeypot CSV (bundled in `data/`), replays rows on
a timer, and exposes them (WebSocket stream + a ts-store schema store, likely).

## Open questions for Tom

- Replay cadence (real-time-ish vs. accelerated)?
- Stream over WebSocket (like the existing sim) AND/OR write to a ts-store
  schema store for historical query?
- Globe: show individual flying arcs (event stream) vs. static weighted arcs
  (aggregated)? Probably both modes.
