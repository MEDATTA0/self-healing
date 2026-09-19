from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Query

from dto import PredictHealthDto, PredictHealthOnlyDto, PredictHealthOnlyResponse
from schemas import HealthDecisionArgs
from services import PredictService


class PredictController:
    """HTTP layer for the `/predict` routes; the logic lives in `PredictService`."""

    def __init__(self, service: PredictService) -> None:
        self._service = service

        self.router = APIRouter(prefix="/predict")
        self.router.add_api_route("", self.predict, methods=["GET"])
        self.router.add_api_route(
            "/health-only", self.predict_health_only, methods=["GET"]
        )

    def predict(self, dto: Annotated[PredictHealthDto, Query()]) -> dict:
        """Run both models and return self-healing decision.

        Inputs are raw metrics; engineered features are computed internally.
        """
        decision = self._service.predict(HealthDecisionArgs(**dto.model_dump()))

        response = {
            "health_state": decision.health_state,
            "model_health_state": decision.model_health_state,
            "health_adjusted_by_guardrail": decision.guardrail_applied,
            "health_confidence": round(decision.health_confidence, 4),
            "action_model_decision": decision.action_model_decision,
            "action_confidence": round(decision.action_confidence, 4),
            "final_recommended_action": decision.final_recommended_action,
            "engineered_features": decision.engineered_features,
        }
        print(response)
        return response

    def predict_health_only(
        self, dto: Annotated[PredictHealthOnlyDto, Query()]
    ) -> PredictHealthOnlyResponse:
        """Run only the health classifier and return health-state decision."""
        decision = self._service.predict_health(HealthDecisionArgs(**dto.model_dump()))

        return PredictHealthOnlyResponse(
            health_state=decision.health_state,
            model_health_state=decision.model_health_state,
            health_adjusted_by_guardrail=decision.guardrail_applied,
            health_confidence=round(decision.health_confidence, 4),
            engineered_features=decision.engineered_features,
        )
