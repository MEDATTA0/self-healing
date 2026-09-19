from __future__ import annotations

from pathlib import Path

from fastapi import FastAPI

from controllers import PredictController
from services import PredictService

BASE_DIR = Path(__file__).parent
MODELS_DIR = BASE_DIR / "models"

app = FastAPI(
    title="Self-Healing Model API",
    version="1.0.0",
    description="FastAPI wrapper for health and action model predictions.",
)


@app.get("/")
def root() -> dict:
    return {
        "service": "self-healing-model-api",
        "status": "ok",
        "predict_endpoint": "/predict",
        "health_only_endpoint": "/predict/health-only",
        "swagger_api_documentation": "/docs",
    }


app.include_router(PredictController(PredictService(MODELS_DIR)).router)
