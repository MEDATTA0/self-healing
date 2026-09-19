from dataclasses import dataclass


@dataclass(frozen=True)
class HealthDecisionArgs:
    cpu_allocation_efficiency: float
    memory_allocation_efficiency: float
    disk_io: float
    network_latency: float
    node_temperature: float
    node_cpu_usage: float
    node_memory_usage: float
    pod_lifetime_seconds: float
    scaling_event: bool
    cpu_request: float
    cpu_limit: float
    memory_request: float
    memory_limit: float
    cpu_usage: float
    memory_usage: float
    restart_count: int
    uptime_seconds: float
    network_bandwidth_usage: float
    namespace: str
    deployment_strategy: str
    scaling_policy: str
