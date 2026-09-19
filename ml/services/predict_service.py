from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from pathlib import Path

import joblib
import pandas as pd
import xgboost as xgb
from fastapi import HTTPException
from sklearn.preprocessing import LabelEncoder

from schemas import FeatureRow, HealthDecision, HealthDecisionArgs, PredictDecision


@dataclass(frozen=True)
class _Artifacts:
    health_model: xgb.XGBClassifier
    action_model: xgb.XGBClassifier
    health_label_encoder: LabelEncoder
    action_label_encoder: LabelEncoder
    feature_label_encoders: dict[str, LabelEncoder]
    feature_names: list[str]


def _load_artifacts(models_dir: Path) -> _Artifacts:
    required_files = [
        models_dir / "health_classifier.json",
        models_dir / "action_classifier.json",
        models_dir / "health_label_encoder.pkl",
        models_dir / "action_label_encoder.pkl",
        models_dir / "feature_label_encoders.pkl",
        models_dir / "feature_names.json",
    ]
    missing = [str(p) for p in required_files if not p.exists()]
    if missing:
        raise FileNotFoundError(
            "Missing model artifacts. Train the models first. Missing: "
            + ", ".join(missing)
        )

    health_model = xgb.XGBClassifier()
    health_model.load_model(str(models_dir / "health_classifier.json"))

    action_model = xgb.XGBClassifier()
    action_model.load_model(str(models_dir / "action_classifier.json"))

    with open(models_dir / "feature_names.json", "r", encoding="utf-8") as f:
        feature_names = json.load(f)

    return _Artifacts(
        health_model=health_model,
        action_model=action_model,
        health_label_encoder=joblib.load(models_dir / "health_label_encoder.pkl"),
        action_label_encoder=joblib.load(models_dir / "action_label_encoder.pkl"),
        feature_label_encoders=joblib.load(models_dir / "feature_label_encoders.pkl"),
        feature_names=feature_names,
    )


def _encode_categorical(artifacts: _Artifacts, column: str, value: str) -> int:
    encoder = artifacts.feature_label_encoders.get(column)
    if encoder is None:
        raise HTTPException(
            status_code=500, detail=f"Missing encoder for column '{column}'"
        )

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


def _predict_label_and_confidence(
    model: xgb.XGBClassifier, label_encoder: LabelEncoder, row: pd.DataFrame
) -> tuple[str, float]:
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

    if (
        model_health_state == "critical"
        and rule_health_state == "normal"
        and low_stress_profile
    ):
        return "normal", True

    # Secondary downgrade path when the model says critical but rules indicate warning.
    if (
        model_health_state == "critical"
        and rule_health_state == "warning"
        and health_confidence < 0.90
    ):
        return "warning", True

    return model_health_state, False


def _build_model_row(
    artifacts: _Artifacts, args: HealthDecisionArgs
) -> tuple[pd.DataFrame, FeatureRow]:
    feature_row = FeatureRow(
        **asdict(args),
        namespace_perf_enc=_encode_categorical(
            artifacts, "namespace_perf", args.namespace
        ),
        deployment_strategy_enc=_encode_categorical(
            artifacts, "deployment_strategy", args.deployment_strategy
        ),
        scaling_policy_enc=_encode_categorical(
            artifacts, "scaling_policy", args.scaling_policy
        ),
    )

    row = pd.DataFrame([feature_row.to_dict()], columns=artifacts.feature_names)
    return row, feature_row


def _decide_health(
    artifacts: _Artifacts, args: HealthDecisionArgs
) -> tuple[HealthDecision, pd.DataFrame]:
    row, features = _build_model_row(artifacts, args)

    model_health_state, health_conf = _predict_label_and_confidence(
        artifacts.health_model, artifacts.health_label_encoder, row
    )
    rule_health_state = _rule_based_health_state(
        features.cpu_pressure_ratio,
        features.memory_pressure_ratio,
        args.restart_count,
        args.node_cpu_usage,
        args.node_memory_usage,
        args.network_latency,
    )
    health_state, guardrail_applied = _apply_health_guardrail(
        model_health_state,
        health_conf,
        rule_health_state,
        features.cpu_pressure_ratio,
        features.memory_pressure_ratio,
        args.restart_count,
        args.node_cpu_usage,
        args.node_memory_usage,
        args.network_latency,
    )
    decision = HealthDecision(
        health_state=health_state,
        model_health_state=model_health_state,
        guardrail_applied=guardrail_applied,
        health_confidence=health_conf,
        engineered_features={
            "cpu_pressure_ratio": round(features.cpu_pressure_ratio, 4),
            "memory_pressure_ratio": round(features.memory_pressure_ratio, 4),
            "cpu_overcommit_ratio": round(features.cpu_overcommit_ratio, 4),
            "mem_overcommit_ratio": round(features.mem_overcommit_ratio, 4),
        },
    )
    return decision, row


class PredictService:
    """Runs the health and action models and applies the self-healing rules.

    Artifacts are loaded once at construction for low-latency inference.
    """

    def __init__(self, models_dir: Path) -> None:
        self._artifacts: _Artifacts | None
        self._load_error = ""
        try:
            self._artifacts = _load_artifacts(models_dir)
        except FileNotFoundError as exc:
            # App can still start, but prediction endpoints return a clear error.
            self._artifacts = None
            self._load_error = str(exc)

    def _require_artifacts(self) -> _Artifacts:
        if self._artifacts is None:
            raise HTTPException(status_code=500, detail=self._load_error)
        return self._artifacts

    def predict_health(self, args: HealthDecisionArgs) -> HealthDecision:
        """Run only the health classifier."""
        decision, _ = _decide_health(self._require_artifacts(), args)
        return decision

    def predict(self, args: HealthDecisionArgs) -> PredictDecision:
        """Run both models and return the self-healing decision."""
        artifacts = self._require_artifacts()
        health, row = _decide_health(artifacts, args)

        action, action_conf = _predict_label_and_confidence(
            artifacts.action_model, artifacts.action_label_encoder, row
        )

        # Guardrail: do not suggest disruptive actions when final health is normal.
        final_action = "none" if health.health_state == "normal" else action
        return PredictDecision(
            **asdict(health),
            action_model_decision=action,
            action_confidence=action_conf,
            final_recommended_action=final_action,
        )
