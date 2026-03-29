#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"

echo "Starting FastAPI on :8010"
cd "$ROOT/ml"
source .venv/bin/activate
nohup uvicorn api:app --host 0.0.0.0 --port 8010 > /tmp/self-healing-fastapi.log 2>&1 &
FASTAPI_PID=$!

echo "Starting collector (air)"
cd "$ROOT/data-collector/collector"
export ML_API_URL='http://localhost:8010'
nohup air main.go > /tmp/self-healing-collector.log 2>&1 &
COLLECTOR_PID=$!

echo "FastAPI PID: $FASTAPI_PID"
echo "Collector PID: $COLLECTOR_PID"
echo "Logs: /tmp/self-healing-fastapi.log, /tmp/self-healing-collector.log"
