#!/bin/bash
# Cleanup Script for Old Server (100.74.102.38)
# Run this AFTER verifying the new server is working correctly

set -e

OLD_HOST="100.74.102.38"
OLD_USER="${OLD_USER:-$(whoami)}"

echo "=== Cleanup Old Simulator Server ==="
echo "Target: ${OLD_USER}@${OLD_HOST}"
echo ""
echo "WARNING: This will stop all simulator services on the old server."
echo "Make sure the new server (100.127.19.27) is working correctly first!"
echo ""
read -p "Press Enter to continue or Ctrl+C to abort..."

# Stop simulator services
echo "[1/2] Stopping simulator services..."
ssh ${OLD_USER}@${OLD_HOST} "cd ~/simulators && docker compose down" || echo "  (simulators may not exist)"

# Stop ts-store
echo "[2/2] Stopping ts-store..."
ssh ${OLD_USER}@${OLD_HOST} "cd ~/ts-store && docker compose down" || echo "  (ts-store may not exist)"

echo ""
echo "=== Cleanup Complete ==="
echo ""
echo "Services stopped on ${OLD_HOST}."
echo "Data volumes have NOT been deleted. To fully remove:"
echo "  ssh ${OLD_USER}@${OLD_HOST} \"docker volume prune\""
