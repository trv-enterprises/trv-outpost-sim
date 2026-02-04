# Dashboard Data Source Simulators

A collection of Go-based data source simulators for testing dashboard integrations. Provides WebSocket streaming, REST API, PostgreSQL timeseries data, and CSV file sources.

## Quick Start

```bash
# Start all simulators
make up

# View logs
make logs

# Stop all
make down
```

## Services & Endpoints

**Simulators (LXD container - 100.70.88.83 via Tailscale):**

| Service | Endpoint | Description |
|---------|----------|-------------|
| ts-store | `http://100.70.88.83:21080` | Time-series circular buffer store |
| WebSocket | `ws://100.70.88.83:21081/ws` | Real-time sensor readings |
| REST API | `http://100.70.88.83:21082/api/*` | RESTful sensor data |
| CSV | `http://100.70.88.83:21083/sensor_readings.csv` | Static CSV file |
| PostgreSQL | `100.70.88.83:21432` | Timeseries database |

**Kubernetes/Monitoring (trv-srv-001 - 100.127.19.27):**

| Service | Endpoint | Description |
|---------|----------|-------------|
| k3s API | `https://100.127.19.27:6443` | Kubernetes API server |
| Prometheus | `http://100.127.19.27:21090` | Prometheus UI & API |
| Grafana | `http://100.127.19.27:21030` | Grafana dashboards (admin/admin) |

The k3s cluster runs `kube-prometheus-stack` which includes Prometheus, Grafana, Alertmanager, node-exporter, and kube-state-metrics. Available metrics include `node_cpu_seconds_total`, `node_memory_MemAvailable_bytes`, `container_cpu_usage_seconds_total`, etc.

**Local Development (localhost):**

| Service | Endpoint | Description |
|---------|----------|-------------|
| ts-store | `http://localhost:21080` | Time-series circular buffer store |
| WebSocket | `ws://localhost:21081/ws` | Real-time sensor readings |
| REST API | `http://localhost:21082/api/*` | RESTful sensor data |
| CSV | `http://localhost:21083/sensor_readings.csv` | Static CSV file |
| PostgreSQL | `localhost:21432` | Timeseries database |

## LXD Container Setup

The simulators run in an LXD container on trv-srv-001 with its own Tailscale IP (100.70.88.83). This keeps the simulators isolated from production services on the host.

### Container Management

```bash
# SSH to host server
ssh tviviano@100.127.19.27

# List containers
lxc list

# Access container shell
lxc exec simulators -- bash

# View simulator logs
lxc exec simulators -- docker compose -f /root/simulators/docker-compose.yml logs -f

# Restart simulators
lxc exec simulators -- docker compose -f /root/simulators/docker-compose.yml restart

# Stop/start container
lxc stop simulators
lxc start simulators
```

### Updating ts-store Binary

The ts-store binary is downloaded from GitHub releases. To update to the latest version:

```bash
# Get latest release version
curl -s https://api.github.com/repos/trv-enterprises/ts-store/releases/latest | jq -r '.tag_name'

# Update Dockerfile.tsstore with new version URL, then rebuild
lxc exec simulators -- bash -c 'cd /root/simulators && docker compose build tsstore && docker compose up -d tsstore'
```

Or download the latest binary directly:
```bash
curl -sL $(curl -s https://api.github.com/repos/trv-enterprises/ts-store/releases/latest | jq -r '.assets[] | select(.name | contains("amd64")) | .browser_download_url') -o tsstore
```

## WebSocket Simulator

Broadcasts sensor readings at a configurable interval.

**Connection:** `ws://localhost:21081/ws` (or `ws://100.70.88.83:21081/ws` on the LXD container)

**Message Format:**
```json
{
  "timestamp": 1699999999999,
  "sensor_id": "sensor-001",
  "sensor_type": "temperature",
  "value": 22.45,
  "unit": "°C",
  "location": "Building-A/Floor-1",
  "status": "normal",
  "quality": 98
}
```

**Configuration (via WebSocket):**
```json
{"command": "set_interval", "interval": 500}
```

**Configuration (via HTTP):**
```bash
# Get config
curl http://localhost:21081/config

# Update interval
curl -X POST http://localhost:21081/config \
  -H "Content-Type: application/json" \
  -d '{"interval_ms": 500}'
```

## REST API Simulator

Provides RESTful access to sensor readings with pagination and filtering.

**Endpoints:**

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/readings` | All readings (paginated) |
| GET | `/api/readings/latest` | Latest from each sensor |
| GET | `/api/readings/{sensor_id}` | Readings for sensor |
| GET | `/api/sensors` | List all sensors |
| GET | `/api/sensors/{sensor_id}` | Sensor details |
| GET | `/api/stats` | Statistics |
| GET | `/health` | Health check |

**Query Parameters:**
- `limit` - Number of results (default: 100)
- `offset` - Pagination offset
- `sensor_id` - Filter by sensor
- `start_time` - Filter by start time (RFC3339)
- `end_time` - Filter by end time (RFC3339)

**Example:**
```bash
curl "http://localhost:21082/api/readings?limit=10&sensor_id=sensor-001"
```

## PostgreSQL Database

Timeseries database with historical sensor data.

**Connection:**
```
Host: localhost (or 100.70.88.83 on the LXD container)
Port: 21432
User: postgres
Password: postgres
Database: sensors
```

**Tables:**
- `sensors` - Sensor metadata
- `sensor_readings` - Timeseries readings

**Views:**
- `latest_readings` - Latest reading per sensor
- `sensor_stats` - Hourly aggregations

**Sample Queries:**
```sql
-- Latest readings
SELECT * FROM latest_readings;

-- Readings from last hour
SELECT * FROM sensor_readings
WHERE timestamp > NOW() - INTERVAL '1 hour'
ORDER BY timestamp DESC;

-- Hourly averages
SELECT * FROM sensor_stats
WHERE sensor_id = 'sensor-001'
ORDER BY hour DESC
LIMIT 24;

-- Temperature warnings
SELECT r.*, s.sensor_type
FROM sensor_readings r
JOIN sensors s ON r.sensor_id = s.sensor_id
WHERE s.sensor_type = 'temperature'
  AND r.status = 'warning';
```

## CSV File

Static CSV available via HTTP.

**URL:** `http://localhost:21083/sensor_readings.csv`

**Columns:**
- `timestamp` - ISO 8601 timestamp
- `sensor_id` - Sensor identifier
- `sensor_type` - Type (temperature, humidity, etc.)
- `value` - Reading value
- `unit` - Unit of measurement
- `location` - Sensor location
- `quality` - Data quality (0-100)
- `status` - normal/warning/error

## Configuration

Environment variables for docker-compose:

| Variable | Default | Description |
|----------|---------|-------------|
| `WS_INTERVAL_MS` | 1000 | WebSocket broadcast interval |
| `WS_NUM_SENSORS` | 5 | Number of WebSocket sensors |
| `WS_ANOMALY_RATE` | 0.02 | Rate of anomalous readings |
| `NUM_SENSORS` | 10 | Database sensors count |
| `DAYS_BACK` | 7 | Days of historical data |
| `INTERVAL_SECONDS` | 60 | Seconds between DB readings |
| `API_BUFFER_SIZE` | 1000 | REST API buffer size |
| `API_GENERATE_MS` | 5000 | REST API generation interval |

**Example:**
```bash
WS_INTERVAL_MS=500 WS_NUM_SENSORS=10 make up
```

## Testing

```bash
# Test WebSocket (requires websocat)
make test-ws

# Test REST API
make test-api

# Test PostgreSQL
make test-sql

# Test CSV
make test-csv
```

## Development

Run simulators locally without Docker:

```bash
# WebSocket
make dev-ws

# REST API
make dev-api

# Database seeder (requires local PostgreSQL)
make dev-seed
```

## Dashboard Configuration Examples

### WebSocket Datasource
```json
{
  "name": "Live Sensors",
  "type": "socket",
  "config": {
    "socket": {
      "url": "ws://100.70.88.83:21081/ws",
      "protocol": "websocket",
      "message_format": "json",
      "reconnect_on_error": true,
      "reconnect_delay": 1000
    }
  }
}
```

### SQL Datasource
```json
{
  "name": "Sensor Database",
  "type": "sql",
  "config": {
    "sql": {
      "driver": "postgres",
      "connection_string": "host=100.70.88.83 port=21432 user=postgres password=postgres dbname=sensors sslmode=disable"
    }
  }
}
```

### API Datasource
```json
{
  "name": "Sensor API",
  "type": "api",
  "config": {
    "api": {
      "url": "http://100.70.88.83:21082/api/readings/latest",
      "method": "GET"
    }
  }
}
```

### CSV Datasource
```json
{
  "name": "Historical CSV",
  "type": "csv",
  "config": {
    "csv": {
      "path": "http://100.70.88.83:21083/sensor_readings.csv",
      "has_header": true,
      "delimiter": ","
    }
  }
}
```

## Utilities

### Generate Large CSV Files
```bash
go run scripts/generate-csv.go -output data/large_dataset.csv -rows 10000 -sensors 10
```

## Project Structure
```
simulators/
├── websocket/          # WebSocket streaming simulator
│   └── main.go
├── rest-api/           # REST API simulator
│   └── main.go
├── db-seed/            # PostgreSQL seeder
│   ├── main.go
│   └── init.sql
├── data/               # Static data files (CSV)
│   └── sensor_readings.csv
├── scripts/            # Utility scripts
│   └── generate-csv.go
├── docker-compose.yml
├── Dockerfile.*        # Service Dockerfiles
├── Makefile
├── nginx.conf
├── go.mod
└── go.sum
```

## Sensor Types

The simulators generate data for these sensor types:

| Type | Unit | Base Value | Range |
|------|------|------------|-------|
| temperature | °C | 22.0 | -20 to 60 |
| humidity | % | 50.0 | 0 to 100 |
| pressure | hPa | 1013.25 | 970 to 1050 |
| co2 | ppm | 450.0 | 300 to 2000 |
| light | lux | 400.0 | 0 to 2000 |
| voltage | V | 120.0 | 110 to 130 |
| current | A | 15.0 | 0 to 30 |
| power | W | 1800.0 | 0 to 5000 |
| vibration | mm/s | 2.5 | 0 to 10 |
| flow_rate | L/min | 75.0 | 0 to 200 |

## Data Patterns

- **Sinusoidal base**: Values follow a daily cycle pattern
- **Random noise**: Configurable noise added to readings
- **Anomalies**: Configurable rate of warning/error states
- **Quality scores**: 0-100 indicating data reliability
