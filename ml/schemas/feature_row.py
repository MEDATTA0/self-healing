from dataclasses import asdict, dataclass, field


def _safe_ratio(numerator: float, denominator: float) -> float:
    if denominator == 0:
        return 0.0
    return numerator / denominator


@dataclass
class FeatureRow:
    # 1. Base input fields (passed during instantiation)
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

    # 2. Categorical encodings (passed during instantiation, since they depend on
    # the label encoders loaded by the API at startup)
    deployment_strategy_enc: int
    scaling_policy_enc: int
    namespace_perf_enc: int

    # 3. Computed fields (derived automatically in __post_init__)
    scaling_event_int: int = field(init=False)
    cpu_pressure_ratio: float = field(init=False)
    memory_pressure_ratio: float = field(init=False)
    cpu_overcommit_ratio: float = field(init=False)
    mem_overcommit_ratio: float = field(init=False)

    def __post_init__(self):
        self.scaling_event_int = int(self.scaling_event)

        # Calculated ratios
        self.cpu_pressure_ratio = _safe_ratio(self.cpu_usage, self.cpu_limit)
        self.memory_pressure_ratio = _safe_ratio(self.memory_usage, self.memory_limit)
        self.cpu_overcommit_ratio = _safe_ratio(self.cpu_request, self.cpu_limit)
        self.mem_overcommit_ratio = _safe_ratio(self.memory_request, self.memory_limit)

    def to_dict(self) -> dict:
        return asdict(self)
