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

| Component        | Language         | Port | Role                                                                                      |
| ---------------- | ---------------- | ---- | ----------------------------------------------------------------------------------------- |
| `app`            | Node.js          | 3000 | Sample HTTP service with JSON logging                                                     |
| `app-1`          | Go               | 8080 | Sample HTTP service with structured logging                                               |
| `data-collector` | Go               | 1323 | Collects metrics/logs, calls ML API, applies remediations                                 |
| `ml/train.py`    | Python           | —    | Trains XGBoost health + action classifiers                                                |
| `ml/api.py`      | Python (FastAPI) | 8010 | Serves health/action predictions with rule-based guardrails (controller → service layers) |

---

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/) configured against a running cluster (e.g. minikube, kind, or a cloud cluster)
- [Helm 3](https://helm.sh/docs/intro/install/)
- Go >= 1.25 (`go.mod` targets 1.25.6)
- Python 3.14 and [uv](https://docs.astral.sh/uv/) for the ML service (`ml/pyproject.toml`, `ml/.python-version`). The Docker image uses Python 3.12 with `ml/requirements.txt` instead.
- [air](https://github.com/air-verse/air) — live-reload runner used by `start_services.sh` to run the collector
- Node.js 24 — only if you run `app` outside the cluster (its image is `node:24-alpine`)
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) in the cluster, which the HorizontalPodAutoscalers need (on minikube: `minikube addons enable metrics-server`)

---

## Quick Start (Local)

Three scripts cover the local workflow, plus a one-off training step on a fresh clone:

```bash
# 1. Install all dependencies and verify the build
./setup_integration.sh

# 2. Train the models (fresh clone only, see the note below)
ml/.venv/bin/python ml/train.py

# 3. Start the ML API and data collector in the background
./start_services.sh

# 4. (Optional) Smoke-test both services
./test_integration.sh
```

`setup_integration.sh` creates the Python virtual environment under `ml/.venv`, installs `ml/requirements.txt` with pip, downloads Go module dependencies, and runs `go build ./...` to confirm the build is clean. (A TODO in the script tracks moving this step to uv; for now use the uv workflow below if you prefer it.)

> **Why train on a fresh clone?** The XGBoost models (`*.json`) are committed, but the label-encoder pickles (`*.pkl`) are git-ignored. Without them the API still starts, but `/predict` returns HTTP 500 with `Missing model artifacts. Train the models first.`

`start_services.sh` launches the FastAPI server on `:8010` (logs to `/tmp/self-healing-fastapi.log`) and the collector via `air` on `:1323` (logs to `/tmp/self-healing-collector.log`).

`test_integration.sh` checks the FastAPI root, the collector `/health` endpoint, and a sample `/predict` call — all must return HTTP 200.

---

## Manual Local Setup

### 1. ML API

The ML API must be running before the data collector can make predictions.

**With uv (recommended)** — dependencies come from `ml/pyproject.toml` and `ml/uv.lock`:

```bash
cd ml
uv sync                      # creates ml/.venv (Python 3.14)
uv run python train.py       # required on a fresh clone; see the note in Quick Start
uv run uvicorn api:app --host 0.0.0.0 --port 8010
```

**With pip** — `requirements.txt` is the pip-compatible list used by `setup_integration.sh` and the Dockerfile:

```bash
cd ml
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python train.py
uvicorn api:app --host 0.0.0.0 --port 8010
```

Training reads the two datasets in `ml/dataset/` (15k rows each) and writes the models and encoders to `ml/models/`.

Verify it is running:

```bash
curl http://localhost:8010/
# {"service":"self-healing-model-api","status":"ok",...}
```

**With Docker** — `ml/compose.yml` builds the image and serves the API on `:8010`. The image bakes in `ml/models/` at build time, so train first:

```bash
cd ml
docker compose up --build
```

#### How the ML API is organised

```
api.py                     app + wiring only
  └── controllers/         PredictController — the /predict routes (HTTP layer)
        └── services/      PredictService — loads model artifacts once, builds features,
                           runs both classifiers, applies the rule-based guardrails
dto/                       HTTP query/response models (validation)
schemas/                   internal dataclasses passed between the layers
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
- `GET /collect` — manually trigger a collection cycle (also runs the healing pass)
- `GET /k8s/nodes` — list cluster nodes
- `GET /k8s/pods` — list pods

The collector runs an automated collection loop in the background: the first pass starts 10 seconds after startup, then every 2 minutes. Each pass gathers pod metrics from Prometheus and logs from the Kubernetes API, appends the unified data points to `ml_training_dataset.csv` in its working directory, calls the ML API for a health prediction per pod, and applies any recommended remediation.

**Remediation behaviour** (`internals/ml.go`):

| `final_recommended_action` | What the collector does                                         |
| -------------------------- | --------------------------------------------------------------- |
| `restart_pod`              | Deletes the pod so its controller recreates it                  |
| `scale_up`                 | Adds one replica to the pod's Deployment, capped at 10 replicas |
| `none`, `investigate`      | Nothing (logged only)                                           |

- Only pods in the `default` namespace are evaluated.
- Each pod + action pair has a 3-minute cooldown to avoid flapping.
- The `scaling_policy` feature sent to the model is `Auto` when a HorizontalPodAutoscaler targets the pod's Deployment, otherwise `Manual`. `deployment_strategy` is read from the Deployment.

> **Note:** The data collector requires `kubectl` configured against a live cluster and Prometheus reachable at `http://localhost:9090` (hard-coded in `internals/metrics-collector.go`). See the Kubernetes setup section below.

---

## Kubernetes Deployment

### 1. Set Up Prometheus

Two helper scripts are provided. Use whichever matches your preference:

```bash
# Option A — creates the namespace automatically (Helm release: prom-stack)
bash prom-setup.k8s.sh

# Option B — step-by-step with namespace created separately (Helm release: prometheus)
bash prometheus.k8s.sh
```

Or run the Helm commands manually:

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
kubectl create namespace monitoring
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring
```

The collector talks to Prometheus on `localhost:9090`, so keep a port-forward open while it runs. The service name follows the Helm release name (confirm with `kubectl get svc -n monitoring`):

```bash
# release "prometheus" (Option B / manual commands; the last line of prometheus.k8s.sh)
kubectl port-forward -n monitoring svc/prometheus-kube-prometheus-prometheus 9090:9090

# release "prom-stack" (Option A)
kubectl port-forward -n monitoring svc/prom-stack-kube-prometheus-prometheus 9090:9090
```

### 2. Deploy the Sample Apps

`app` and `app-1` are minimal HTTP servers used to give the cluster something to monitor. Build their images and deploy them to Kubernetes — there is no need to run them locally when you already have pods on your local cluster.

```bash
docker build -t self-healing-node-server:v2 app/
docker build -t self-healing-go-server:v2 app-1/
```

If your cluster cannot pull local images directly (e.g. minikube), load them first:

```bash
minikube image load self-healing-node-server:v2
minikube image load self-healing-go-server:v2
```

Then deploy. Each app has a `k8s/` folder with a Deployment, a Service, and an autoscaler:

```bash
kubectl apply -f app/k8s/
kubectl apply -f app-1/k8s/
```

| App     | Deployment              | Service (LoadBalancer)        | HorizontalPodAutoscaler |
| ------- | ----------------------- | ----------------------------- | ----------------------- |
| `app`   | `my-app` (2 replicas)   | `my-app` → nodePort `30000`   | `my-app-autoscaler`     |
| `app-1` | `my-app-1` (2 replicas) | `my-app-1` → nodePort `31000` | `my-app-1-autoscaler`   |

Both autoscalers keep 2–10 replicas and scale up when average CPU exceeds 60% or average memory exceeds 80% (this needs metrics-server; see Prerequisites).

**Note**: The setup above is already enough for a minimal setup; the next part is optional — consider it in case you want to deploy the Data Collector into the cluster as well.

### (Optional) 3. Deploy the Data Collector

> **Known limitation:** the in-cluster deployment is not fully wired up yet. `go-client.k8s.yml` injects the ML API URL as `PYTHON_URL`, but the collector reads `ML_API_URL` (falling back to `http://localhost:8010`), and the Prometheus address is hard-coded to `http://localhost:9090`. Until those are configurable, the reliable way to run the collector against a cluster is locally, as described above. (`python-config.k8s.yml` at the repo root defines a ConfigMap with a different name and key and is not used by these steps.)

```bash
# Build the collector image (the manifest expects an image named "go-client")
docker build -t go-client data-collector/collector/
# minikube only: minikube image load go-client

# Create RBAC resources (ServiceAccount + ClusterRole)
kubectl apply -f data-collector/collector/go-rbac.k8s.yaml

# Create ConfigMap pointing to the ML API
kubectl create configmap python-url \
  --from-literal=python-config=http://<ML_API_HOST>:8010 \
  -n monitoring

# Deploy the collector
kubectl apply -f data-collector/collector/go-client.k8s.yml
```

Replace `<ML_API_HOST>` with the address where the ML API is reachable from inside the cluster (e.g. a ClusterIP service name or external IP). There is no manifest for the ML API itself; run it with `docker compose` (see above) or your own Deployment.

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

| Parameter                      | Type        | Description                                     |
| ------------------------------ | ----------- | ----------------------------------------------- |
| `cpu_allocation_efficiency`    | float >= 0  | CPU allocation efficiency ratio                 |
| `memory_allocation_efficiency` | float >= 0  | Memory allocation efficiency ratio              |
| `disk_io`                      | float >= 0  | Disk I/O (MB/s)                                 |
| `network_latency`              | float >= 0  | Network latency (ms)                            |
| `node_temperature`             | float       | Node temperature (°C)                           |
| `node_cpu_usage`               | float 0–100 | Node-level CPU usage (%)                        |
| `node_memory_usage`            | float 0–100 | Node-level memory usage (%)                     |
| `pod_lifetime_seconds`         | float >= 0  | Pod age in seconds                              |
| `scaling_event`                | bool        | Whether a scaling event occurred                |
| `cpu_request`                  | float >= 0  | CPU request (cores)                             |
| `cpu_limit`                    | float >= 0  | CPU limit (cores)                               |
| `memory_request`               | float >= 0  | Memory request (Mi)                             |
| `memory_limit`                 | float >= 0  | Memory limit (Mi)                               |
| `cpu_usage`                    | float >= 0  | Actual CPU usage (cores)                        |
| `memory_usage`                 | float >= 0  | Actual memory usage (Mi)                        |
| `restart_count`                | int >= 0    | Pod restart count                               |
| `uptime_seconds`               | float >= 0  | Pod uptime in seconds                           |
| `network_bandwidth_usage`      | float >= 0  | Network bandwidth usage (MB/s)                  |
| `namespace`                    | string      | One of: `default`, `dev`, `kube-system`, `prod` |
| `deployment_strategy`          | string      | One of: `Recreate`, `RollingUpdate`             |
| `scaling_policy`               | string      | One of: `Auto`, `Manual`                        |

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

**Errors:** `422` for a missing or invalid parameter; `500` if the model artifacts are missing (train first).

### `GET /predict/health-only`

Same parameters as `/predict` but only runs the health classifier. Returns `health_state`, `model_health_state`, `health_adjusted_by_guardrail`, `health_confidence`, and `engineered_features`.

---

## Project Structure

```
self-healing/
├── app/                        # Node.js sample service
│   ├── index.js
│   ├── Dockerfile
│   └── k8s/                    # app.k8s.yml, lb.k8s.yml, hpa.k8s.yml
├── app-1/                      # Go sample service
│   ├── main.go
│   ├── middleware.go
│   ├── Dockerfile
│   └── k8s/                    # app.k8s.yml, lb.k8s.yml, hpa.k8s.yml
├── data-collector/
│   ├── collector/              # Go data collector & remediation agent
│   │   ├── main.go             # HTTP server + 2-minute collection loop
│   │   ├── internals/
│   │   │   ├── metrics-collector.go
│   │   │   ├── logs-collector.go
│   │   │   ├── dataset-builder.go
│   │   │   ├── ml.go           # MLTalker: inference + remediation
│   │   │   ├── k8s.service.go
│   │   │   └── k8s.routes.go
│   │   ├── pkg/                # Kubernetes client, helpers, collector interfaces
│   │   ├── Dockerfile
│   │   ├── go-rbac.k8s.yaml
│   │   └── go-client.k8s.yml
│   ├── diagrams/               # Mermaid diagrams (class, deployment, sequence, use case)
│   └── node-exporter.k8s.yml   # Standalone node-exporter DaemonSet + Service
├── ml/
│   ├── api.py                  # FastAPI app + wiring
│   ├── controllers/            # PredictController (HTTP layer)
│   ├── services/               # PredictService (model loading, features, guardrails)
│   ├── dto/                    # HTTP query/response models
│   ├── schemas/                # Internal dataclasses
│   ├── train.py                # XGBoost training pipeline
│   ├── pyproject.toml          # uv project (Python 3.14)
│   ├── uv.lock
│   ├── requirements.txt        # pip list used by setup_integration.sh and the Dockerfile
│   ├── Dockerfile
│   ├── compose.yml             # Runs the ML API on :8010
│   ├── dataset/                # Kaggle training datasets (15k rows each)
│   └── models/                 # Trained models & encoders (generated by train.py)
├── setup_integration.sh        # Install deps & verify build (run once)
├── start_services.sh           # Start ML API + collector in background
├── test_integration.sh         # Smoke-test both services
├── prom-setup.k8s.sh           # Prometheus Helm install (auto-creates namespace)
├── prometheus.k8s.sh           # Prometheus Helm install (manual namespace)
├── python-config.k8s.yml       # ConfigMap (not used by the steps above)
├── *.mmd                       # Mermaid diagrams (class, deployment, sequence, use case)
└── locustfile.py               # Locust load test
```
