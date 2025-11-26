.PHONY: all build up down clean logs help test-ws test-api test-sql test-csv seed

# Default configuration
WS_INTERVAL_MS ?= 1000
WS_NUM_SENSORS ?= 5
NUM_SENSORS ?= 10
DAYS_BACK ?= 7

help:
	@echo "Dashboard Data Source Simulators"
	@echo ""
	@echo "Usage:"
	@echo "  make up              - Start all simulators"
	@echo "  make down            - Stop all simulators"
	@echo "  make clean           - Stop and remove all data"
	@echo "  make logs            - View logs from all services"
	@echo "  make build           - Build Docker images"
	@echo "  make seed            - Re-seed database with fresh data"
	@echo ""
	@echo "Testing:"
	@echo "  make test-ws         - Test WebSocket connection"
	@echo "  make test-api        - Test REST API"
	@echo "  make test-sql        - Test SQL database"
	@echo "  make test-csv        - Test CSV file server"
	@echo ""
	@echo "Configuration (via environment variables):"
	@echo "  WS_INTERVAL_MS       - WebSocket broadcast interval (default: 1000)"
	@echo "  WS_NUM_SENSORS       - Number of WebSocket sensors (default: 5)"
	@echo "  NUM_SENSORS          - Number of DB sensors (default: 10)"
	@echo "  DAYS_BACK            - Days of historical data (default: 7)"
	@echo ""
	@echo "Endpoints:"
	@echo "  WebSocket:   ws://localhost:8081/ws"
	@echo "  REST API:    http://localhost:8082/api/readings"
	@echo "  PostgreSQL:  localhost:5432 (sensors db)"
	@echo "  CSV:         http://localhost:8083/sensor_readings.csv"

all: build up

build:
	docker compose build

up:
	WS_INTERVAL_MS=$(WS_INTERVAL_MS) \
	WS_NUM_SENSORS=$(WS_NUM_SENSORS) \
	NUM_SENSORS=$(NUM_SENSORS) \
	DAYS_BACK=$(DAYS_BACK) \
	docker compose up -d
	@echo ""
	@echo "Services started! Endpoints:"
	@echo "  WebSocket:   ws://localhost:8081/ws"
	@echo "  REST API:    http://localhost:8082/api/readings"
	@echo "  PostgreSQL:  localhost:5432 (user: postgres, pass: postgres, db: sensors)"
	@echo "  CSV:         http://localhost:8083/sensor_readings.csv"

down:
	docker compose down

clean:
	docker compose down -v
	@echo "All data volumes removed"

logs:
	docker compose logs -f

logs-ws:
	docker compose logs -f websocket-simulator

logs-api:
	docker compose logs -f rest-api-simulator

logs-db:
	docker compose logs -f postgres

seed:
	docker compose up -d postgres
	@echo "Waiting for PostgreSQL..."
	@sleep 5
	docker compose up db-seeder
	@echo "Database seeded!"

# Testing targets
test-ws:
	@echo "Testing WebSocket (press Ctrl+C to stop)..."
	@command -v websocat >/dev/null 2>&1 && websocat ws://localhost:8081/ws || \
		(echo "websocat not installed. Install with: brew install websocat" && \
		 echo "Or test with: curl http://localhost:8081/health")

test-api:
	@echo "Testing REST API..."
	@echo "\n=== Health Check ==="
	curl -s http://localhost:8082/health | jq .
	@echo "\n=== Sensors List ==="
	curl -s http://localhost:8082/api/sensors | jq .
	@echo "\n=== Latest Readings ==="
	curl -s http://localhost:8082/api/readings/latest | jq .
	@echo "\n=== Readings (limit 5) ==="
	curl -s "http://localhost:8082/api/readings?limit=5" | jq .

test-sql:
	@echo "Testing PostgreSQL..."
	@docker compose exec -T postgres psql -U postgres -d sensors -c "SELECT COUNT(*) as total_readings FROM sensor_readings;"
	@docker compose exec -T postgres psql -U postgres -d sensors -c "SELECT * FROM sensors LIMIT 5;"
	@docker compose exec -T postgres psql -U postgres -d sensors -c "SELECT * FROM latest_readings LIMIT 5;"

test-csv:
	@echo "Testing CSV server..."
	curl -s http://localhost:8083/sensor_readings.csv | head -20

# Development targets
dev-ws:
	cd websocket && go run main.go -port 8081 -interval $(WS_INTERVAL_MS) -sensors $(WS_NUM_SENSORS)

dev-api:
	cd rest-api && go run main.go -port 8082

dev-seed:
	cd db-seed && go run main.go -host localhost -port 5432 -user postgres -password postgres -dbname sensors

# Go module management
mod-tidy:
	go mod tidy

mod-download:
	go mod download
