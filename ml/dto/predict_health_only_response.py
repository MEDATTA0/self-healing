from dataclasses import dataclass


@dataclass(frozen=True)
class PredictHealthOnlyResponse:
    health_state: str
    model_health_state: str
    health_adjusted_by_guardrail: bool
    health_confidence: float
    engineered_features: dict
