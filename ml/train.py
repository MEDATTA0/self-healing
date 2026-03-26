"""
Self-Healing K8s — Model Training Pipeline
============================================

Two XGBoost classifiers trained on the Kaggle Kubernetes datasets:

  Model 1 — Health Classifier
    Input  : performance metrics (CPU/memory efficiency, latency, disk I/O, node stress)
    Output : health_state  →  normal | warning | critical
    Source : kubernetes_performance_metrics_dataset.csv

  Model 2 — Action Classifier
    Input  : merged performance + allocation features + engineered pressure ratios
    Output : recommended_action  →  none | restart_pod | scale_up | investigate
    Source : both datasets joined on pod_name

Inference sequence (runtime):
  health_state = health_classifier.predict(metrics)
  if health_state != "normal":
      action = action_classifier.predict(metrics)

Saved artefacts (ml/models/):
  health_classifier.json          — XGBoost model (portable JSON)
  action_classifier.json          — XGBoost model (portable JSON)
  health_label_encoder.pkl        — LabelEncoder for health_state classes
  action_label_encoder.pkl        — LabelEncoder for recommended_action classes
  feature_label_encoders.pkl      — LabelEncoders for categorical input columns
  feature_names.json              — Ordered feature list expected at inference time
"""

import json
import joblib
import warnings
import numpy as np
import pandas as pd
import xgboost as xgb
from pathlib import Path
from sklearn.model_selection import train_test_split, StratifiedKFold, cross_val_score
from sklearn.preprocessing import LabelEncoder
from sklearn.metrics import classification_report, confusion_matrix, f1_score

warnings.filterwarnings("ignore")

# ── Paths ──────────────────────────────────────────────────────────────────────
BASE_DIR   = Path(__file__).parent
DATA_DIR   = BASE_DIR / "dataset"
MODELS_DIR = BASE_DIR / "models"
MODELS_DIR.mkdir(exist_ok=True)

PERF_CSV  = DATA_DIR / "kubernetes_performance_metrics_dataset.csv"
ALLOC_CSV = DATA_DIR / "kubernetes_resource_allocation_dataset.csv"

RANDOM_STATE = 42
TEST_SIZE    = 0.20
CV_FOLDS     = 5

# ── 1. Load ────────────────────────────────────────────────────────────────────
print("=" * 62)
print("  Loading datasets")
print("=" * 62)

perf  = pd.read_csv(PERF_CSV)
alloc = pd.read_csv(ALLOC_CSV)

print(f"  Performance  : {perf.shape[0]:,} rows x {perf.shape[1]} cols")
print(f"  Allocation   : {alloc.shape[0]:,} rows x {alloc.shape[1]} cols")

# ── 2. Merge on pod_name ───────────────────────────────────────────────────────
print("\n" + "=" * 62)
print("  Merging datasets on pod_name")
print("=" * 62)

# Both files have 15k unique pod names (pod_0 … pod_14999) — 1:1 join.
df = pd.merge(perf, alloc, on="pod_name", suffixes=("_perf", "_alloc"))
print(f"  Merged       : {df.shape[0]:,} rows x {df.shape[1]} cols")

# ── 3. Feature Engineering ─────────────────────────────────────────────────────
print("\n" + "=" * 62)
print("  Feature engineering")
print("=" * 62)

# Resource pressure ratios — the most direct self-healing signals
df["cpu_pressure_ratio"]    = df["cpu_usage"]    / df["cpu_limit"].replace(0, np.nan)
df["memory_pressure_ratio"] = df["memory_usage"] / df["memory_limit"].replace(0, np.nan)
df["cpu_overcommit_ratio"]  = df["cpu_request"]  / df["cpu_limit"].replace(0, np.nan)
df["mem_overcommit_ratio"]  = df["memory_request"] / df["memory_limit"].replace(0, np.nan)
df.fillna(0, inplace=True)

# Convert scaling_event from string to int (TRUE → 1, FALSE → 0)
df["scaling_event_int"] = (df["scaling_event"].astype(str).str.upper() == "TRUE").astype(int)

# Encode categorical columns
CAT_COLS = ["namespace_perf", "deployment_strategy", "scaling_policy"]
feature_label_encoders: dict = {}
for col in CAT_COLS:
    if col in df.columns:
        le = LabelEncoder()
        df[col + "_enc"] = le.fit_transform(df[col].astype(str))
        feature_label_encoders[col] = le
        print(f"  Encoded '{col}'  → {le.classes_.tolist()}")

# ── 4. Feature List ────────────────────────────────────────────────────────────
FEATURES = [
    # --- Performance signals ---
    "cpu_allocation_efficiency",
    "memory_allocation_efficiency",
    "disk_io",
    "network_latency",
    "node_temperature",
    "node_cpu_usage",
    "node_memory_usage",
    "pod_lifetime_seconds",
    "scaling_event_int",
    # --- Allocation signals ---
    "cpu_request",
    "cpu_limit",
    "memory_request",
    "memory_limit",
    "cpu_usage",
    "memory_usage",
    "restart_count",
    "uptime_seconds",
    "network_bandwidth_usage",
    # --- Engineered pressure ratios ---
    "cpu_pressure_ratio",
    "memory_pressure_ratio",
    "cpu_overcommit_ratio",
    "mem_overcommit_ratio",
    # --- Encoded categoricals ---
    "namespace_perf_enc",
    "deployment_strategy_enc",
    "scaling_policy_enc",
]

# Keep only columns that actually exist after all engineering steps
FEATURES = [f for f in FEATURES if f in df.columns]
X = df[FEATURES]

print(f"\n  Final feature count: {len(FEATURES)}")
print(f"  Features: {FEATURES}")

# ── 5. Label Engineering ───────────────────────────────────────────────────────
print("\n" + "=" * 62)
print("  Label engineering")
print("=" * 62)

# NOTE: The Kaggle `event_type` column (Normal/Warning/Error) was randomly
# generated independently of the numeric metrics in this synthetic dataset —
# there is no statistical correlation. Training on it produces ~33% accuracy
# (random chance on a balanced 3-class problem).
#
# Both labels are therefore derived entirely from numeric feature thresholds,
# the same rule-based approach used in data-collector/internals/dataset-builder.go.
# This creates a genuinely learnable signal that the model can fit.

# Model 1 label — health_state
# Priority: critical > warning > normal
def _health_label(row) -> str:
    status  = row["pod_status"]
    msg     = row["event_message"]
    cpu_p   = row["cpu_pressure_ratio"]
    mem_p   = row["memory_pressure_ratio"]
    restarts = row["restart_count"]
    node_cpu = row["node_cpu_usage"]
    node_mem = row["node_memory_usage"]
    latency  = row["network_latency"]

    # critical: pod has crashed, OOM'd, or resources are exhausted
    if (status in ("Failed", "Unknown")
            or msg in ("OOMKilled", "Killed", "Failed")
            or cpu_p > 1.0
            or mem_p > 1.0
            or restarts >= 7):
        return "critical"

    # warning: pod is under stress but still alive
    if (node_cpu > 70.0
            or node_mem > 70.0
            or latency > 150.0
            or cpu_p > 0.75
            or mem_p > 0.75
            or restarts >= 3
            or status == "Pending"):
        return "warning"

    return "normal"

df["health_state"] = df.apply(_health_label, axis=1)

print("\n  health_state distribution:")
print(df["health_state"].value_counts().to_string())

# Model 2 label — recommended_action
# Priority order: investigate > restart_pod > scale_up > none
def _action_label(row) -> str:
    status   = row["pod_status"]
    msg      = row["event_message"]
    restarts = row["restart_count"]
    cpu_p    = row["cpu_pressure_ratio"]
    mem_p    = row["memory_pressure_ratio"]

    # Unknown/Pending — state is unclear, human review first
    if status in ("Unknown", "Pending"):
        return "investigate"

    # Crashed or OOM — restart is the correct first action
    if status == "Failed" or msg in ("OOMKilled", "Killed", "Failed"):
        return "restart_pod"

    # Running but starving for resources — horizontal/vertical scale
    if status == "Running" and (cpu_p > 1.0 or mem_p > 1.0):
        return "scale_up"

    # Frequent restarts without hard failure — needs investigation
    if restarts >= 5 and status != "Succeeded":
        return "investigate"

    return "none"

df["recommended_action"] = df.apply(_action_label, axis=1)

print("\n  recommended_action distribution:")
print(df["recommended_action"].value_counts().to_string())


# ── 6. Shared Train / Test Split ───────────────────────────────────────────────
print("\n" + "=" * 62)
print("  Train / Test split (80 / 20, stratified)")
print("=" * 62)

# We use health_state as the primary stratification key.
X_train, X_test, idx_train, idx_test = train_test_split(
    X, df.index,
    test_size=TEST_SIZE,
    random_state=RANDOM_STATE,
    stratify=df["health_state"],
)

y1_train = df.loc[idx_train, "health_state"]
y1_test  = df.loc[idx_test,  "health_state"]
y2_train = df.loc[idx_train, "recommended_action"]
y2_test  = df.loc[idx_test,  "recommended_action"]

print(f"  Train: {len(X_train):,}   Test: {len(X_test):,}")


# ── 7. Helper: train & evaluate one XGBoost classifier ────────────────────────
def train_and_evaluate(
    name: str,
    X_tr: pd.DataFrame,
    y_tr: pd.Series,
    X_te: pd.DataFrame,
    y_te: pd.Series,
    le: LabelEncoder,
) -> xgb.XGBClassifier:

    y_tr_enc = le.fit_transform(y_tr)
    y_te_enc = le.transform(y_te)

    n_classes = len(le.classes_)
    objective = "binary:logistic" if n_classes == 2 else "multi:softprob"

    # Compute per-sample weights to balance any class imbalance
    class_counts = np.bincount(y_tr_enc)
    class_weights = len(y_tr_enc) / (n_classes * class_counts)
    sample_weights = class_weights[y_tr_enc]

    model = xgb.XGBClassifier(
        n_estimators=500,
        max_depth=6,
        learning_rate=0.05,
        subsample=0.8,
        colsample_bytree=0.8,
        min_child_weight=3,
        gamma=0.1,
        reg_alpha=0.1,
        reg_lambda=1.0,
        objective=objective,
        num_class=n_classes if n_classes > 2 else None,
        eval_metric="mlogloss" if n_classes > 2 else "logloss",
        random_state=RANDOM_STATE,
        n_jobs=-1,
        early_stopping_rounds=20,
    )

    model.fit(
        X_tr, y_tr_enc,
        sample_weight=sample_weights,
        eval_set=[(X_te, y_te_enc)],
        verbose=False,
    )

    y_pred = model.predict(X_te)
    y_pred_labels = le.inverse_transform(y_pred)

    print(f"\n  — {name} —")
    print(classification_report(y_te, y_pred_labels, zero_division=0))

    print(f"  Confusion matrix  (classes: {le.classes_.tolist()})")
    cm = confusion_matrix(y_te, y_pred_labels, labels=le.classes_)
    cm_df = pd.DataFrame(cm, index=le.classes_, columns=le.classes_)
    print(cm_df.to_string())

    weighted_f1 = f1_score(y_te, y_pred_labels, average="weighted", zero_division=0)
    print(f"\n  Weighted F1: {weighted_f1:.4f}")

    # Top feature importances
    imp = pd.Series(model.feature_importances_, index=FEATURES).sort_values(ascending=False)
    print(f"\n  Top 10 features:")
    print(imp.head(10).to_string())

    return model


# ── 8. Train Model 1: Health Classifier ───────────────────────────────────────
print("\n" + "=" * 62)
print("  Model 1 — Health Classifier")
print("=" * 62)

health_le    = LabelEncoder()
health_model = train_and_evaluate(
    "Health Classifier",
    X_train, y1_train,
    X_test,  y1_test,
    health_le,
)

# ── 9. Train Model 2: Action Classifier ───────────────────────────────────────
print("\n" + "=" * 62)
print("  Model 2 — Action Classifier")
print("=" * 62)

action_le    = LabelEncoder()
action_model = train_and_evaluate(
    "Action Classifier",
    X_train, y2_train,
    X_test,  y2_test,
    action_le,
)

# ── 10. Cross-validation ──────────────────────────────────────────────────────
print("\n" + "=" * 62)
print(f"  Cross-validation ({CV_FOLDS}-fold, stratified)")
print("=" * 62)

y1_full_enc = health_le.transform(df["health_state"])
y2_full_enc = action_le.transform(df["recommended_action"])

# Re-initialise models without early_stopping for CV (needs fixed n_estimators)
health_model_cv = xgb.XGBClassifier(
    n_estimators=health_model.best_iteration + 1,
    max_depth=6, learning_rate=0.05,
    subsample=0.8, colsample_bytree=0.8,
    min_child_weight=3, gamma=0.1,
    reg_alpha=0.1, reg_lambda=1.0,
    objective="multi:softprob", num_class=len(health_le.classes_),
    eval_metric="mlogloss", random_state=RANDOM_STATE, n_jobs=-1,
)
action_model_cv = xgb.XGBClassifier(
    n_estimators=action_model.best_iteration + 1,
    max_depth=6, learning_rate=0.05,
    subsample=0.8, colsample_bytree=0.8,
    min_child_weight=3, gamma=0.1,
    reg_alpha=0.1, reg_lambda=1.0,
    objective="multi:softprob", num_class=len(action_le.classes_),
    eval_metric="mlogloss", random_state=RANDOM_STATE, n_jobs=-1,
)

cv = StratifiedKFold(n_splits=CV_FOLDS, shuffle=True, random_state=RANDOM_STATE)

h_scores = cross_val_score(health_model_cv, X, y1_full_enc, cv=cv, scoring="f1_weighted", n_jobs=-1)
a_scores = cross_val_score(action_model_cv, X, y2_full_enc, cv=cv, scoring="f1_weighted", n_jobs=-1)

print(f"\n  Health Classifier  F1(weighted): {h_scores.mean():.4f} ± {h_scores.std():.4f}")
print(f"  Action Classifier  F1(weighted): {a_scores.mean():.4f} ± {a_scores.std():.4f}")

# ── 11. Save artefacts ────────────────────────────────────────────────────────
print("\n" + "=" * 62)
print("  Saving model artefacts")
print("=" * 62)

health_model.save_model(str(MODELS_DIR / "health_classifier.json"))
action_model.save_model(str(MODELS_DIR / "action_classifier.json"))

joblib.dump(health_le,              MODELS_DIR / "health_label_encoder.pkl")
joblib.dump(action_le,              MODELS_DIR / "action_label_encoder.pkl")
joblib.dump(feature_label_encoders, MODELS_DIR / "feature_label_encoders.pkl")

with open(MODELS_DIR / "feature_names.json", "w") as f:
    json.dump(FEATURES, f, indent=2)

# Save a model card for quick reference
model_card = {
    "health_classifier": {
        "target": "health_state",
        "classes": health_le.classes_.tolist(),
        "best_iteration": int(health_model.best_iteration),
        "cv_f1_weighted": round(float(h_scores.mean()), 4),
        "cv_f1_std": round(float(h_scores.std()), 4),
    },
    "action_classifier": {
        "target": "recommended_action",
        "classes": action_le.classes_.tolist(),
        "best_iteration": int(action_model.best_iteration),
        "cv_f1_weighted": round(float(a_scores.mean()), 4),
        "cv_f1_std": round(float(a_scores.std()), 4),
    },
    "features": FEATURES,
    "train_rows": int(len(X_train)),
    "test_rows":  int(len(X_test)),
}

with open(MODELS_DIR / "model_card.json", "w") as f:
    json.dump(model_card, f, indent=2)

print(f"\n  Saved to: {MODELS_DIR}/")
for p in sorted(MODELS_DIR.iterdir()):
    print(f"    {p.name}")

print("\n" + "=" * 62)
print("  Training complete.")
print("=" * 62)
