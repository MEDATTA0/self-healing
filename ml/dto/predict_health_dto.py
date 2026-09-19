from typing import Literal

from pydantic import BaseModel, Field


class PredictHealthDto(BaseModel):
    cpu_allocation_efficiency: float = Field(..., ge=0.0)
    memory_allocation_efficiency: float = Field(..., ge=0.0)
    disk_io: float = Field(..., ge=0.0)
    network_latency: float = Field(..., ge=0.0)
    node_temperature: float = Field(...)
    node_cpu_usage: float = Field(..., ge=0.0, le=100.0)
    node_memory_usage: float = Field(..., ge=0.0, le=100.0)
    pod_lifetime_seconds: float = Field(..., ge=0.0)
    scaling_event: bool = Field(..., description="true or false")
    cpu_request: float = Field(..., ge=0.0)
    cpu_limit: float = Field(..., ge=0.0)
    memory_request: float = Field(..., ge=0.0)
    memory_limit: float = Field(..., ge=0.0)
    cpu_usage: float = Field(..., ge=0.0)
    memory_usage: float = Field(..., ge=0.0)
    restart_count: int = Field(..., ge=0)
    uptime_seconds: float = Field(..., ge=0.0)
    network_bandwidth_usage: float = Field(..., ge=0.0)
    namespace: Literal["default", "dev", "kube-system", "prod"] = Field(...)
    deployment_strategy: Literal["Recreate", "RollingUpdate"] = Field(...)
    scaling_policy: Literal["Auto", "Manual"] = Field(...)
