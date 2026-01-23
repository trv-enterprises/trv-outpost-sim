# ts-store Integration Plan for WebSocket Simulator

## Overview

This plan outlines the strategy for integrating ts-store (a circular time series database) with the WebSocket simulator to:
1. Store WebSocket sensor data in ts-store
2. Allow the WebSocket simulator to read from ts-store when clients connect
3. Deploy ts-store as a Docker container alongside the simulator stack

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         SIMULATOR STACK                                      │
│                                                                              │
│  ┌──────────────────┐     ┌──────────────────┐     ┌──────────────────┐    │
│  │   Data Writer    │────▶│    ts-store      │◀────│  WebSocket       │    │
│  │   (new service)  │     │   Port: 8084     │     │  Simulator       │    │
│  │                  │     │                  │     │  Port: 8081      │    │
│  │  Generates data  │     │  Circular store  │     │                  │    │
│  │  every N seconds │     │  Single block    │     │  Reads from      │    │
│  │                  │     │  per reading     │     │  ts-store when   │    │
│  └──────────────────┘     └──────────────────┘     │  client connects │    │
│                                                     └──────────────────┘    │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Data Format

### Current WebSocket Message Format
```json
{
  "timestamp": 1704067200000,
  "sensor_id": "sensor-001",
  "sensor_type": "temperature",
  "value": 70.35,
  "unit": "°F",
  "location": "Building-A",
  "status": "normal",
  "quality": 95
}
```

### ts-store Storage Format
- **Timestamp**: Convert from milliseconds to nanoseconds (`timestamp * 1_000_000`)
- **Data**: Store as JSON using `/api/stores/:store/json` endpoint
- **Block Size**: 512 bytes (sufficient for single sensor reading ~200 bytes)

## Store Configuration

### Sizing Calculations

**Requirements**:
- 50 sensors × 1 reading/second = 50 readings/second
- Each reading ~200 bytes JSON
- Target retention: 1 hour of data = 180,000 readings

**Configuration**:
```json
{
  "name": "sensor-readings",
  "num_blocks": 200000,
  "data_block_size": 512,
  "index_block_size": 4096
}
```

**Storage footprint**:
- Data file: 200,000 × 2 × 512 = ~200MB
- Index file: ~5MB
- Total: ~205MB

This provides ~1+ hour retention with automatic circular reclamation.

## Implementation Steps

### Phase 1: Deploy ts-store Container

**1.1 Create Dockerfile for ts-store** (to be done in ts-store repo)

```dockerfile
# Dockerfile for ts-store
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /tsstore ./cmd/tsstore

FROM alpine:3.18
RUN apk --no-cache add ca-certificates
COPY --from=builder /tsstore /usr/local/bin/
EXPOSE 8080
VOLUME /data
CMD ["tsstore", "serve"]
```

**1.2 Add ts-store to simulators docker-compose.yml**

```yaml
# Add to simulators/docker-compose.yml
tsstore:
  image: tsstore:latest  # Or build from context
  container_name: sensor-tsstore
  ports:
    - "8084:8080"
  environment:
    TSSTORE_HOST: "0.0.0.0"
    TSSTORE_PORT: 8080
    TSSTORE_DATA_PATH: /data
  volumes:
    - tsstore_data:/data
  healthcheck:
    test: ["CMD", "wget", "-q", "--spider", "http://localhost:8080/health"]
    interval: 10s
    timeout: 5s
    retries: 3
  networks:
    - simulator-net

volumes:
  tsstore_data:
```

### Phase 2: Create Data Writer Service

A lightweight Go service that:
1. Generates sensor data (reuse existing generation logic)
2. Writes to ts-store via REST API
3. Runs continuously

**2.1 File: `simulators/data-writer/main.go`**

Key functions:
- Initialize connection to ts-store
- Create store if not exists (on startup)
- Generate sensor readings using same algorithm as current simulator
- POST each reading to ts-store's JSON endpoint
- Handle reconnection on failure

**2.2 Configuration**:
```go
type Config struct {
    TSStoreURL     string        // http://tsstore:8080
    StoreName      string        // sensor-readings
    NumSensors     int           // 50
    IntervalMS     int           // 1000
    EnableNoise    bool          // true
    AnomalyRate    float64       // 0.02
}
```

### Phase 3: Modify WebSocket Simulator

Update the WebSocket simulator to:
1. Read from ts-store instead of generating data
2. Stream newest readings to connected clients
3. Fall back to generated data if ts-store unavailable

**3.1 New data flow**:
```
Client connects to WebSocket
    │
    ▼
WebSocket server polls ts-store for newest readings
(GET /api/stores/sensor-readings/json/newest?limit=50)
    │
    ▼
Broadcast readings to all connected clients
    │
    ▼
Sleep for interval, repeat
```

**3.2 Key changes to `websocket/main.go`**:

```go
// New function to fetch from ts-store
func fetchFromTSStore(client *http.Client, baseURL, storeName, apiKey string) ([]SensorReading, error) {
    url := fmt.Sprintf("%s/api/stores/%s/json/newest?limit=50", baseURL, storeName)
    req, _ := http.NewRequest("GET", url, nil)
    req.Header.Set("X-API-Key", apiKey)

    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var readings []SensorReading
    json.NewDecoder(resp.Body).Decode(&readings)
    return readings, nil
}

// Modified broadcast loop
func broadcastLoop() {
    client := &http.Client{Timeout: 5 * time.Second}

    for {
        readings, err := fetchFromTSStore(client, tsStoreURL, storeName, apiKey)
        if err != nil {
            // Fallback to generated data
            readings = generateReadings()
        }

        broadcast(readings)
        time.Sleep(interval)
    }
}
```

### Phase 4: Startup Sequence

**4.1 Docker Compose startup order**:
```yaml
services:
  tsstore:
    # Starts first

  data-writer:
    depends_on:
      tsstore:
        condition: service_healthy
    # Creates store and starts writing

  websocket-simulator:
    depends_on:
      tsstore:
        condition: service_healthy
    # Reads from ts-store, serves WebSocket clients
```

**4.2 Store initialization** (in data-writer):
```go
func initializeStore() error {
    // Check if store exists
    resp, err := http.Get(tsStoreURL + "/api/stores")
    // ...

    // Create if not exists
    storeConfig := map[string]interface{}{
        "name":            "sensor-readings",
        "num_blocks":      200000,
        "data_block_size": 512,
        "index_block_size": 4096,
    }

    resp, err = http.Post(tsStoreURL + "/api/stores",
        "application/json",
        bytes.NewReader(configJSON))

    // Save API key for later use
    // API key is returned only once during creation
}
```

## API Key Management

Since ts-store API keys are shown only once during store creation:

**Option A: Store in file (development)**
- Data writer creates store, saves API key to shared volume
- WebSocket simulator reads API key from shared volume

**Option B: Environment variable (production)**
- Create store manually, set API key as env var
- Both services read from environment

**Recommended**: Option A for automatic setup in dev, Option B for production.

## File Structure

```
simulators/
├── docker-compose.yml          # Add tsstore service
├── data-writer/
│   ├── Dockerfile
│   ├── main.go                 # Data generation + ts-store writer
│   └── go.mod
├── websocket/
│   └── main.go                 # Modified to read from ts-store
└── tsstore/
    └── config.json             # ts-store configuration
```

## Implementation Priority

| Priority | Task | Description |
|----------|------|-------------|
| 1 | Docker support for ts-store | Create Dockerfile in ts-store repo |
| 2 | Add ts-store to docker-compose | Configure container, volume, network |
| 3 | Create data-writer service | New service to write to ts-store |
| 4 | Modify websocket simulator | Read from ts-store instead of generating |
| 5 | API key management | Implement key sharing between services |
| 6 | Testing | End-to-end verification |

## Alternative: Simpler Single-Service Approach

Instead of a separate data-writer, the WebSocket simulator could:
1. Generate data internally (as now)
2. Write to ts-store in same process
3. Serve WebSocket clients from ts-store reads

This reduces complexity but couples the components more tightly.

```go
// Combined approach in websocket simulator
func broadcastLoop() {
    for {
        // Generate readings
        readings := generateReadings()

        // Store in ts-store
        for _, r := range readings {
            storeReading(r)
        }

        // Broadcast to WebSocket clients
        broadcast(readings)

        time.Sleep(interval)
    }
}
```

## Configuration Summary

| Service | Port | Purpose |
|---------|------|---------|
| ts-store | 8084 (host) / 8080 (container) | Circular time series storage |
| data-writer | N/A | Generates and stores sensor data |
| websocket-simulator | 8081 | Serves WebSocket clients |

## Next Steps

1. **ts-store team**: Add Dockerfile and docker-compose example to ts-store repo
2. **Simulator team**:
   - Update docker-compose.yml with ts-store service
   - Create data-writer service OR modify websocket simulator
   - Test integration end-to-end
