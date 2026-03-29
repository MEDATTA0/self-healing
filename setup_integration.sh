#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"

echo "[1/3] Python deps"
cd "$ROOT/ml"
python3 -m venv .venv || true
source .venv/bin/activate
pip install -q -r requirements.txt

echo "[2/3] Go deps"
cd "$ROOT/data-collector/collector"
go mod download

echo "[3/3] Build check"
go build ./...

echo "Setup complete"
