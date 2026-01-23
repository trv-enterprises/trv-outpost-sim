#!/bin/bash
# Simulator Migration Script
# Migrates all simulator services to trv-srv-001 (100.127.19.27)

set -e

TARGET_HOST="100.127.19.27"
TARGET_USER="${TARGET_USER:-$(whoami)}"
LOCAL_SIMULATORS="/Users/tviviano/Documents/GitHub/dashboard/simulators"
LOCAL_TSSTORE="/Users/tviviano/Documents/GitHub/ts-store"

echo "=== Simulator Migration to trv-srv-001 ==="
echo "Target: ${TARGET_USER}@${TARGET_HOST}"
echo ""

# Step 1: Prepare target server
echo "[1/8] Preparing target server directories..."
ssh ${TARGET_USER}@${TARGET_HOST} "mkdir -p ~/simulators ~/ts-store"

# Step 2: Copy ts-store files (to sibling directory)
echo "[2/8] Copying ts-store files..."
rsync -avz --exclude '.git' --exclude 'bin' --exclude '*.exe' \
    "${LOCAL_TSSTORE}/" ${TARGET_USER}@${TARGET_HOST}:~/ts-store/

# Step 3: Copy simulator files
echo "[3/8] Copying simulator files..."
rsync -avz --exclude '.git' --exclude 'node_modules' --exclude '__pycache__' \
    "${LOCAL_SIMULATORS}/" ${TARGET_USER}@${TARGET_HOST}:~/simulators/

# Step 4: Set TSSTORE_BUILD_CONTEXT for remote directory structure
echo "[4/8] Configuring environment for remote build context..."
ssh ${TARGET_USER}@${TARGET_HOST} "cd ~/simulators && echo 'TSSTORE_BUILD_CONTEXT=../ts-store' >> .env"

# Step 5: Build and start services
echo "[5/8] Building and starting services..."
ssh ${TARGET_USER}@${TARGET_HOST} "cd ~/simulators && docker compose up -d --build"

# Step 6: Wait for ts-store to be healthy
echo "[6/8] Waiting for ts-store to be healthy..."
sleep 10
MAX_ATTEMPTS=30
ATTEMPT=0
until ssh ${TARGET_USER}@${TARGET_HOST} "curl -s http://localhost:21080/health" 2>/dev/null | grep -q "ok"; do
    ATTEMPT=$((ATTEMPT + 1))
    if [ $ATTEMPT -ge $MAX_ATTEMPTS ]; then
        echo "  ERROR: ts-store did not become healthy after ${MAX_ATTEMPTS} attempts"
        exit 1
    fi
    echo "  Waiting for ts-store... (attempt ${ATTEMPT}/${MAX_ATTEMPTS})"
    sleep 5
done
echo "  ts-store is healthy!"

# Step 7: Create ts-store datastore
echo "[7/8] Creating ts-store datastore..."
STORE_RESPONSE=$(ssh ${TARGET_USER}@${TARGET_HOST} "curl -s -X POST http://localhost:21080/api/stores \
    -H 'Content-Type: application/json' \
    -d '{\"name\": \"sensor-readings\", \"block_count\": 200000, \"block_size\": 512}'" 2>/dev/null)
echo "  Store creation response: ${STORE_RESPONSE}"

# Step 8: Seed PostgreSQL database
echo "[8/8] Seeding PostgreSQL database..."
ssh ${TARGET_USER}@${TARGET_HOST} "cd ~/simulators && docker compose up db-seeder"

echo ""
echo "=== Migration Complete ==="
echo ""
echo "Port mappings:"
echo "  ts-store:    http://${TARGET_HOST}:21080"
echo "  WebSocket:   ws://${TARGET_HOST}:21081/ws"
echo "  REST API:    http://${TARGET_HOST}:21082"
echo "  CSV Server:  http://${TARGET_HOST}:21083"
echo "  PostgreSQL:  ${TARGET_HOST}:21432"
echo ""
echo "Verification commands:"
echo "  ssh ${TARGET_USER}@${TARGET_HOST} 'docker ps --filter label=project=simulators'"
echo "  curl http://${TARGET_HOST}:21080/health"
echo "  curl http://${TARGET_HOST}:21081/health"
echo "  curl http://${TARGET_HOST}:21082/health"
echo "  websocat ws://${TARGET_HOST}:21081/ws"
echo ""
echo "Next steps:"
echo "  1. Verify all services are working correctly"
echo "  2. Update any dashboard data sources to use new URLs"
echo "  3. Run cleanup-old-server.sh to stop services on 100.74.102.38"
