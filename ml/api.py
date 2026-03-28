from __future__ import annotations

import json
from pathlib import Path
from typing import Literal

import joblib
import pandas as pd
import xgboost as xgb
from fastapi import FastAPI, HTTPException, Query

BASE_DIR = Path(__file__).parent
MODELS_DIR = BASE_DIR / "models"

app = FastAPI(
    title="Self-Healing Model API",
    version="1.0.0",
    description="FastAPI wrapper for health and action model predictions.",
)


# Load models and metadata once at startup for low-latency inference.
def _load_artifacts() -> tuple[
    xgb.XGBClassifier,
    xgb.XGBClassifier,
    object,
    object,
    dict,
    list[str],
]:
    required_files = [
        MODELS_DIR / "health_classifier.json",
        MODELS_DIR / "action_classifier.json",
        MODELS_DIR / "health_label_encoder.pkl",
        MODELS_DIR / "action_label_encoder.pkl",
        MODELS_DIR / "feature_label_encoders.pkl",
        MODELS_DIR / "feature_names.json",
    ]
    missing = [str(p) for p in required_files if not p.exists()]
    if missing:
        raise FileNotFoundError(
            "Missing model artifacts. Train the models first. Missing: " + ", ".join(missing)
        )

    health_model = xgb.XGBClassifier()
    health_model.load_model(str(MODELS_DIR / "health_classifier.json"))

    action_model = xgb.XGBClassifier()
    action_model.load_model(str(MODELS_DIR / "action_classifier.json"))

    health_le = joblib.load(MODELS_DIR / "health_label_encoder.pkl")
    action_le = joblib.load(MODELS_DIR / "action_label_encoder.pkl")
    feature_label_encoders = joblib.load(MODELS_DIR / "feature_label_encoders.pkl")

    with open(MODELS_DIR / "feature_names.json", "r", encoding="utf-8") as f:
        feature_names = json.load(f)

    return health_model, action_model, health_le, action_le, feature_label_encoders, feature_names


try:
    (
        HEALTH_MODEL,
        ACTION_MODEL,
        HEALTH_LE,
        ACTION_LE,
        FEATURE_LABEL_ENCODERS,
        FEATURE_NAMES,
    ) = _load_artifacts()
except FileNotFoundError as exc:
    # App can still start, but prediction endpoint will return a clear error.
    HEALTH_MODEL = None
    ACTION_MODEL = None
    HEALTH_LE = None
    ACTION_LE = None
    FEATURE_LABEL_ENCODERS = None
    FEATURE_NAMES = None
    LOAD_ERROR = str(exc)
else:
    LOAD_ERROR = ""


def _safe_ratio(numerator: float, denominator: float) -> float:
    if denominator == 0:
        return 0.0
    return numerator / denominator


def _encode_categorical(column: str, value: str) -> int:
    encoder = FEATURE_LABEL_ENCODERS.get(column)
    if encoder is None:
        raise HTTPException(status_code=500, detail=f"Missing encoder for column '{column}'")

    classes = set(encoder.classes_)
    if value not in classes:
        raise HTTPException(
            status_code=400,
            detail=(
                f"Invalid value '{value}' for '{column}'. "
                f"Allowed values: {sorted(classes)}"
            ),
        )

    return int(encoder.transform([value])[0])


def _predict_label_and_confidence(model: xgb.XGBClassifier, label_encoder, row: pd.DataFrame) -> tuple[str, float]:
    probs = model.predict_proba(row)[0]
    idx = int(probs.argmax())
    label = str(label_encoder.inverse_transform([idx])[0])
    confidence = float(probs[idx])
    return label, confidence


def _rule_based_health_state(
    cpu_pressure_ratio: float,
    memory_pressure_ratio: float,
    restart_count: int,
    node_cpu_usage: float,
    node_memory_usage: float,
    network_latency: float,
) -> str:
    # Mirrors training-time thresholds using only runtime-available signals.
    if cpu_pressure_ratio > 1.0 or memory_pressure_ratio > 1.0 or restart_count >= 7:
        return "critical"

    if (
        node_cpu_usage > 70.0
        or node_memory_usage > 70.0
        or network_latency > 150.0
        or cpu_pressure_ratio > 0.75
        or memory_pressure_ratio > 0.75
        or restart_count >= 3
    ):
        return "warning"

    return "normal"


def _apply_health_guardrail(
    model_health_state: str,
    health_confidence: float,
    rule_health_state: str,
    cpu_pressure_ratio: float,
    memory_pressure_ratio: float,
    restart_count: int,
    node_cpu_usage: float,
    node_memory_usage: float,
    network_latency: float,
) -> tuple[str, bool]:
    # Prevent false criticals for clearly low-stress pods.
    low_stress_profile = (
        restart_count == 0
        and cpu_pressure_ratio < 0.50
        and memory_pressure_ratio < 0.50
        and node_cpu_usage < 50.0
        and node_memory_usage < 50.0
        and network_latency < 100.0
    )

    if model_health_state == "critical" and rule_health_state == "normal" and low_stress_profile:
        return "normal", True

    # Secondary downgrade path when the model says critical but rules indicate warning.
    if model_health_state == "critical" and rule_health_state == "warning" and health_confidence < 0.90:
        return "warning", True

    return model_health_state, False


@app.get("/")
def root() -> dict:
    return {
        "service": "self-healing-model-api",
        "status": "ok",
        "predict_endpoint": "/predict",
        "health_only_endpoint": "/predict/health-only",
        "swagger_api_documentation": "/docs"
    }


def _build_model_row(
    cpu_allocation_efficiency: float,
    memory_allocation_efficiency: float,
    disk_io: float,
    network_latency: float,
    node_temperature: float,
    node_cpu_usage: float,
    node_memory_usage: float,
    pod_lifetime_seconds: float,
    scaling_event: bool,
    cpu_request: float,
    cpu_limit: float,
    memory_request: float,
    memory_limit: float,
    cpu_usage: float,
    memory_usage: float,
    restart_count: int,
    uptime_seconds: float,
    network_bandwidth_usage: float,
    namespace: str,
    deployment_strategy: str,
    scaling_policy: str,
) -> tuple[pd.DataFrame, float, float, float, float]:
    cpu_pressure_ratio = _safe_ratio(cpu_usage, cpu_limit)
    memory_pressure_ratio = _safe_ratio(memory_usage, memory_limit)
    cpu_overcommit_ratio = _safe_ratio(cpu_request, cpu_limit)
    mem_overcommit_ratio = _safe_ratio(memory_request, memory_limit)

    feature_row = {
        "cpu_allocation_efficiency": cpu_allocation_efficiency,
        "memory_allocation_efficiency": memory_allocation_efficiency,
        "disk_io": disk_io,
        "network_latency": network_latency,
        "node_temperature": node_temperature,
        "node_cpu_usage": node_cpu_usage,
        "node_memory_usage": node_memory_usage,
        "pod_lifetime_seconds": pod_lifetime_seconds,
        "scaling_event_int": int(scaling_event),
        "cpu_request": cpu_request,
        "cpu_limit": cpu_limit,
        "memory_request": memory_request,
        "memory_limit": memory_limit,
        "cpu_usage": cpu_usage,
        "memory_usage": memory_usage,
        "restart_count": restart_count,
        "uptime_seconds": uptime_seconds,
        "network_bandwidth_usage": network_bandwidth_usage,
        "cpu_pressure_ratio": cpu_pressure_ratio,
        "memory_pressure_ratio": memory_pressure_ratio,
        "cpu_overcommit_ratio": cpu_overcommit_ratio,
        "mem_overcommit_ratio": mem_overcommit_ratio,
        "namespace_perf_enc": _encode_categorical("namespace_perf", namespace),
        "deployment_strategy_enc": _encode_categorical("deployment_strategy", deployment_strategy),
        "scaling_policy_enc": _encode_categorical("scaling_policy", scaling_policy),
    }

    row = pd.DataFrame([feature_row], columns=FEATURE_NAMES)
    return row, cpu_pressure_ratio, memory_pressure_ratio, cpu_overcommit_ratio, mem_overcommit_ratio


def _compute_health_decision(
    cpu_allocation_efficiency: float,
    memory_allocation_efficiency: float,
    disk_io: float,
    network_latency: float,
    node_temperature: float,
    node_cpu_usage: float,
    node_memory_usage: float,
    pod_lifetime_seconds: float,
    scaling_event: bool,
    cpu_request: float,
    cpu_limit: float,
    memory_request: float,
    memory_limit: float,
    cpu_usage: float,
    memory_usage: float,
    restart_count: int,
    uptime_seconds: float,
    network_bandwidth_usage: float,
    namespace: str,
    deployment_strategy: str,
    scaling_policy: str,
) -> tuple[pd.DataFrame, str, str, bool, float, dict]:
    row, cpu_pressure_ratio, memory_pressure_ratio, cpu_overcommit_ratio, mem_overcommit_ratio = _build_model_row(
        cpu_allocation_efficiency,
        memory_allocation_efficiency,
        disk_io,
        network_latency,
        node_temperature,
        node_cpu_usage,
        node_memory_usage,
        pod_lifetime_seconds,
        scaling_event,
        cpu_request,
        cpu_limit,
        memory_request,
        memory_limit,
        cpu_usage,
        memory_usage,
        restart_count,
        uptime_seconds,
        network_bandwidth_usage,
        namespace,
        deployment_strategy,
        scaling_policy,
    )

    model_health_state, health_conf = _predict_label_and_confidence(HEALTH_MODEL, HEALTH_LE, row)
    rule_health_state = _rule_based_health_state(
        cpu_pressure_ratio,
        memory_pressure_ratio,
        restart_count,
        node_cpu_usage,
        node_memory_usage,
        network_latency,
    )
    health_state, guardrail_applied = _apply_health_guardrail(
        model_health_state,
        health_conf,
        rule_health_state,
        cpu_pressure_ratio,
        memory_pressure_ratio,
        restart_count,
        node_cpu_usage,
        node_memory_usage,
        network_latency,
    )
    engineered_features = {
        "cpu_pressure_ratio": round(cpu_pressure_ratio, 4),
        "memory_pressure_ratio": round(memory_pressure_ratio, 4),
        "cpu_overcommit_ratio": round(cpu_overcommit_ratio, 4),
        "mem_overcommit_ratio": round(mem_overcommit_ratio, 4),
    }
    return row, health_state, model_health_state, guardrail_applied, health_conf, engineered_features


@app.get("/predict")
def predict(
    cpu_allocation_efficiency: float = Query(..., ge=0.0),
    memory_allocation_efficiency: float = Query(..., ge=0.0),
    disk_io: float = Query(..., ge=0.0),
    network_latency: float = Query(..., ge=0.0),
    node_temperature: float = Query(...),
    node_cpu_usage: float = Query(..., ge=0.0, le=100.0),
    node_memory_usage: float = Query(..., ge=0.0, le=100.0),
    pod_lifetime_seconds: float = Query(..., ge=0.0),
    scaling_event: bool = Query(..., description="true or false"),
    cpu_request: float = Query(..., ge=0.0),
    cpu_limit: float = Query(..., ge=0.0),
    memory_request: float = Query(..., ge=0.0),
    memory_limit: float = Query(..., ge=0.0),
    cpu_usage: float = Query(..., ge=0.0),
    memory_usage: float = Query(..., ge=0.0),
    restart_count: int = Query(..., ge=0),
    uptime_seconds: float = Query(..., ge=0.0),
    network_bandwidth_usage: float = Query(..., ge=0.0),
    namespace: Literal["default", "dev", "kube-system", "prod"] = Query(...),
    deployment_strategy: Literal["Recreate", "RollingUpdate"] = Query(...),
    scaling_policy: Literal["Auto", "Manual"] = Query(...),
) -> dict:
    """Run both models and return self-healing decision.

    Inputs are raw metrics; engineered features are computed internally.
    """
    if LOAD_ERROR:
        raise HTTPException(status_code=500, detail=LOAD_ERROR)

    row, health_state, model_health_state, guardrail_applied, health_conf, engineered_features = _compute_health_decision(
        cpu_allocation_efficiency,
        memory_allocation_efficiency,
        disk_io,
        network_latency,
        node_temperature,
        node_cpu_usage,
        node_memory_usage,
        pod_lifetime_seconds,
        scaling_event,
        cpu_request,
        cpu_limit,
        memory_request,
        memory_limit,
        cpu_usage,
        memory_usage,
        restart_count,
        uptime_seconds,
        network_bandwidth_usage,
        namespace,
        deployment_strategy,
        scaling_policy,
    )

    action, action_conf = _predict_label_and_confidence(ACTION_MODEL, ACTION_LE, row)

    # Guardrail: do not suggest disruptive actions when final health is normal.
    final_action = "none" if health_state == "normal" else action
    response = {
        "health_state": health_state,
        "model_health_state": model_health_state,
        "health_adjusted_by_guardrail": guardrail_applied,
        "health_confidence": round(health_conf, 4),
        "action_model_decision": action,
        "action_confidence": round(action_conf, 4),
        "final_recommended_action": final_action,
        "engineered_features": engineered_features,
    }
    print(response)
    return response


@app.get("/predict/health-only")
def predict_health_only(
    cpu_allocation_efficiency: float = Query(..., ge=0.0),
    memory_allocation_efficiency: float = Query(..., ge=0.0),
    disk_io: float = Query(..., ge=0.0),
    network_latency: float = Query(..., ge=0.0),
    node_temperature: float = Query(...),
    node_cpu_usage: float = Query(..., ge=0.0, le=100.0),
    node_memory_usage: float = Query(..., ge=0.0, le=100.0),
    pod_lifetime_seconds: float = Query(..., ge=0.0),
    scaling_event: bool = Query(..., description="true or false"),
    cpu_request: float = Query(..., ge=0.0),
    cpu_limit: float = Query(..., ge=0.0),
    memory_request: float = Query(..., ge=0.0),
    memory_limit: float = Query(..., ge=0.0),
    cpu_usage: float = Query(..., ge=0.0),
    memory_usage: float = Query(..., ge=0.0),
    restart_count: int = Query(..., ge=0),
    uptime_seconds: float = Query(..., ge=0.0),
    network_bandwidth_usage: float = Query(..., ge=0.0),
    namespace: Literal["default", "dev", "kube-system", "prod"] = Query(...),
    deployment_strategy: Literal["Recreate", "RollingUpdate"] = Query(...),
    scaling_policy: Literal["Auto", "Manual"] = Query(...),
) -> dict:
    """Run only the health classifier and return health-state decision."""
    if LOAD_ERROR:
        raise HTTPException(status_code=500, detail=LOAD_ERROR)

    _, health_state, model_health_state, guardrail_applied, health_conf, engineered_features = _compute_health_decision(
        cpu_allocation_efficiency,
        memory_allocation_efficiency,
        disk_io,
        network_latency,
        node_temperature,
        node_cpu_usage,
        node_memory_usage,
        pod_lifetime_seconds,
        scaling_event,
        cpu_request,
        cpu_limit,
        memory_request,
        memory_limit,
        cpu_usage,
        memory_usage,
        restart_count,
        uptime_seconds,
        network_bandwidth_usage,
        namespace,
        deployment_strategy,
        scaling_policy,
    )
    response = {
        "health_state": health_state,
        "model_health_state": model_health_state,
        "health_adjusted_by_guardrail": guardrail_applied,
        "health_confidence": round(health_conf, 4),
        "engineered_features": engineered_features,
    }

    return response
