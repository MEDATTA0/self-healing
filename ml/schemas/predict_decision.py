from dataclasses import dataclass


@dataclass(frozen=True)
class HealthDecision:
    health_state: str
    model_health_state: str
    guardrail_applied: bool
    health_confidence: float
    engineered_features: dict[str, float]


@dataclass(frozen=True)
class PredictDecision(HealthDecision):
    action_model_decision: str
    action_confidence: float
    final_recommended_action: str
