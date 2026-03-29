#!/usr/bin/env bash
set -euo pipefail

echo "Testing FastAPI root"
curl -fsS http://localhost:8010/ >/dev/null
echo "OK"

echo "Testing collector health"
curl -fsS http://localhost:1323/health >/dev/null
echo "OK"

echo "Testing predict endpoint"
curl -fsS "http://localhost:8010/predict?cpu_allocation_efficiency=0.5&memory_allocation_efficiency=0.5&disk_io=1&network_latency=10&node_temperature=65&node_cpu_usage=20&node_memory_usage=20&pod_lifetime_seconds=100&scaling_event=false&cpu_request=0.3&cpu_limit=0.5&memory_request=100&memory_limit=200&cpu_usage=0.1&memory_usage=60&restart_count=0&uptime_seconds=100&network_bandwidth_usage=1&namespace=default&deployment_strategy=RollingUpdate&scaling_policy=Manual" >/dev/null

echo "All checks passed"
