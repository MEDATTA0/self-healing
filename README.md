# Self-Healing Kubernetes System

An ML-powered self-healing system that monitors Kubernetes workloads, predicts pod health, and automatically remediates issues via restart or scale-up actions.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│ Kubernetes Cluster                                      │
│                                                         │
│  ┌──────────────────┐     ┌──────────────────┐         │
│  │  app (Node.js)   │     │  app-1 (Go)      │         │
│  │  :3000           │     │  :8080           │         │
│  └────────┬─────────┘     └────────┬─────────┘         │
│           │ Pod logs (JSON / CLF)  │                   │
│           └───────────┬────────────┘                   │
│                       ▼                                │
│           ┌───────────────────────┐                   │
│           │  data-collector (Go)  │ ← Prometheus :9090│
│           │  :1323                │                   │
│           │  monitoring namespace │                   │
│           └───────────┬───────────┘                   │
│                       │ /predict                      │
└───────────────────────┼───────────────────────────────┘
                        ▼
            ┌───────────────────────┐
            │  ML API (FastAPI)     │
            │  :8010                │
            │  /predict             │
            │  /predict/health-only │
            └───────────────────────┘
```

**Components:**

| Component | Language | Port | Role |
|-----------|----------|------|------|
| `app` | Node.js | 3000 | Sample HTTP service with JSON logging |
| `app-1` | Go | 8080 | Sample HTTP service with structured logging |
| `data-collector` | Go | 1323 | Collects metrics/logs, calls ML API, applies remediations |
| `ml/train.py` | Python | — | Trains XGBoost health + action classifiers |
| `ml/api.py` | Python (FastAPI) | 8010 | Serves health/action predictions with rule-based guardrails |

---

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/) configured against a running cluster (e.g. minikube, kind, or a cloud cluster)
- [Helm 3](https://helm.sh/docs/intro/install/)
- Go >= 1.21
- Node.js >= 18
- Python >= 3.10

---

## Local Setup

### 1. ML API

The ML API must be running before the data collector can make predictions.

```bash
cd ml
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
```

**Train models** (skip if pre-trained models already exist in `ml/models/`):

```bash
python train.py
```

This reads from `ml/dataset/` and writes trained models to `ml/models/`.

**Start the inference server:**

```bash
uvicorn api:app --host 0.0.0.0 --port 8010
```

Verify it is running:

```bash
curl http://localhost:8010/
# {"service":"self-healing-model-api","status":"ok",...}
```

---

### 2. Data Collector

```bash
cd data-collector/collector
go build -o collector .

# Set the ML API URL (defaults to http://localhost:8010)
export ML_API_URL=http://localhost:8010

./collector
```

The server listens on port `1323`. Available endpoints:

- `GET /health` — health check
- `GET /collect` — manually trigger a collection cycle

The collector runs an automated collection loop every 2 minutes in the background, gathering pod metrics from Prometheus and logs from the Kubernetes API, then calling the ML API for health predictions and applying any recommended remediations.

> **Note:** The data collector requires `kubectl` configured against a live cluster and Prometheus reachable at `http://localhost:9090`. See the Kubernetes setup section below.

---

## Kubernetes Deployment

### 1. Set Up Prometheus

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
kubectl create namespace monitoring
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring
```

### 2. Deploy the Sample Apps

`app` and `app-1` are minimal HTTP servers used to give the cluster something to monitor. Build their images and deploy them to Kubernetes — there is no need to run them locally when you already have pods on you local cluster.

```bash
docker build -t self-healing-app:v4 app/
docker build -t self-healing-app-1:v3 app-1/
```

If your cluster cannot pull local images directly (e.g. minikube), load them first:

```bash
minikube image load self-healing-app:v4
minikube image load self-healing-app-1:v3
```

Then deploy:

```bash
kubectl apply -f app/app.k8s.yml
kubectl apply -f app-1/app.k8s.yml
```

This creates Deployments (2 replicas each) and LoadBalancer Services:

- `app` → nodePort `30000`
- `app-1` → nodePort `31000`

**Note**: The setup is already done for a minimal setup, the next part is optional; consider it in case you want to deploy the Date Collector into the cluster as well.

### (Optional) 3. Deploy the Data Collector

```bash
# Create RBAC resources (ServiceAccount + ClusterRole)
kubectl apply -f data-collector/collector/go-rbac.k8s.yaml

# Create ConfigMap pointing to the ML API
kubectl create configmap python-url \
  --from-literal=python-config=http://<ML_API_HOST>:8010 \
  -n monitoring

# Deploy the collector
kubectl apply -f data-collector/collector/go-client.k8s.yml
```

Replace `<ML_API_HOST>` with the address where the ML API is reachable from inside the cluster (e.g. a ClusterIP service name or external IP).

Verify the collector is running:

```bash
kubectl get pods -n monitoring
kubectl logs -n monitoring deployment/go-client -f
```

---

## Load Testing

`locustfile.py` is included to generate traffic against the sample apps when running locally (e.g. with `kubectl port-forward`). It is not needed for normal cluster operation.

```bash
pip install locust

# Forward the Node.js app to localhost first
kubectl port-forward svc/my-app 3000:3000

# Then run Locust against it
locust -f locustfile.py --host http://localhost:3000
```

Open `http://localhost:8089` for the Locust web UI, or run headless:

```bash
locust -f locustfile.py --host http://localhost:3000 -u 20 -r 5 --headless -t 60s
```

---

## ML API Reference

### `GET /predict`

Runs both the health and action classifiers and returns a self-healing recommendation.

**Required query parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `cpu_allocation_efficiency` | float >= 0 | CPU allocation efficiency ratio |
| `memory_allocation_efficiency` | float >= 0 | Memory allocation efficiency ratio |
| `disk_io` | float >= 0 | Disk I/O (MB/s) |
| `network_latency` | float >= 0 | Network latency (ms) |
| `node_temperature` | float | Node temperature (°C) |
| `node_cpu_usage` | float 0–100 | Node-level CPU usage (%) |
| `node_memory_usage` | float 0–100 | Node-level memory usage (%) |
| `pod_lifetime_seconds` | float >= 0 | Pod age in seconds |
| `scaling_event` | bool | Whether a scaling event occurred |
| `cpu_request` | float >= 0 | CPU request (cores) |
| `cpu_limit` | float >= 0 | CPU limit (cores) |
| `memory_request` | float >= 0 | Memory request (Mi) |
| `memory_limit` | float >= 0 | Memory limit (Mi) |
| `cpu_usage` | float >= 0 | Actual CPU usage (cores) |
| `memory_usage` | float >= 0 | Actual memory usage (Mi) |
| `restart_count` | int >= 0 | Pod restart count |
| `uptime_seconds` | float >= 0 | Pod uptime in seconds |
| `network_bandwidth_usage` | float >= 0 | Network bandwidth usage (MB/s) |
| `namespace` | string | One of: `default`, `dev`, `kube-system`, `prod` |
| `deployment_strategy` | string | One of: `Recreate`, `RollingUpdate` |
| `scaling_policy` | string | One of: `Auto`, `Manual` |

**Example response:**

```json
{
  "health_state": "normal",
  "model_health_state": "critical",
  "health_adjusted_by_guardrail": true,
  "health_confidence": 0.9123,
  "action_model_decision": "restart_pod",
  "action_confidence": 0.7654,
  "final_recommended_action": "none",
  "engineered_features": {
    "cpu_pressure_ratio": 0.4,
    "memory_pressure_ratio": 0.35,
    "cpu_overcommit_ratio": 0.6,
    "mem_overcommit_ratio": 0.5
  }
}
```

- `health_adjusted_by_guardrail: true` means the rule-based guardrail overrode the model prediction.
- `final_recommended_action` is suppressed to `"none"` when `health_state` is `"normal"`.

### `GET /predict/health-only`

Same parameters as `/predict` but only runs the health classifier. Returns `health_state`, `model_health_state`, `health_adjusted_by_guardrail`, `health_confidence`, and `engineered_features`.

---

## Project Structure

```
self-healing/
├── app/                        # Node.js sample service
│   ├── index.js
│   ├── Dockerfile
│   └── app.k8s.yml
├── app-1/                      # Go sample service
│   ├── main.go
│   ├── middleware.go
│   ├── Dockerfile
│   └── app.k8s.yml
├── data-collector/
│   └── collector/              # Go data collector & remediation agent
│       ├── main.go
│       ├── internals/
│       │   ├── metrics-collector.go
│       │   ├── logs-collector.go
│       │   ├── dataset-builder.go
│       │   ├── ml.go           # MLTalker: inference + remediation
│       │   ├── k8s.service.go
│       │   └── k8s.routes.go
│       ├── Dockerfile
│       ├── go-rbac.k8s.yaml
│       └── go-client.k8s.yml
├── ml/
│   ├── train.py                # XGBoost training pipeline
│   ├── api.py                  # FastAPI inference server
│   ├── requirements.txt
│   ├── dataset/                # Kaggle training datasets (15k rows each)
│   └── models/                 # Trained models & encoders (generated by train.py)
├── locustfile.py               # Locust load test
└── prometheus.k8s.sh           # Prometheus Helm install script
```
