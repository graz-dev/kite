# Agent Context: Kite (Kubernetes Intelligent Tuning Engine)

## 1. Vision
Kite is a Kubernetes Operator designed for the automatic rightsizing of workloads, with a specialized focus on Java/JVM applications. It operates as a "Closed-Loop Controller" that bridges the gap between observability data and infrastructure configuration.

The name "Kite" represents its core mechanic: dynamically adjusting its height (resource allocation) based on the wind (workload metrics) while staying tethered to safety constraints.

## 2. Architecture & Design Principles
- **No Native Observability:** Kite does NOT collect metrics. It assumes an external Prometheus instance is populated with data from:
    - **Grafana Beyla** (Network/CPU/System metrics via eBPF).
    - **OpenTelemetry Java Agent** (Internal JVM/JMX metrics).
- **Decoupled Logic:** The operator is a consumer of the Prometheus API. It doesn't care how the data gets there, only that it is available.
- **Resource Synchronization:** Kite is "JVM-Aware". When it modifies a Container Memory Limit, it simultaneously calculates and updates the `-Xmx` (Max Heap) via environment variables to ensure the runtime is aligned with the cgroup limits.
- **Stability First:** To prevent "flapping" (constant restarts), Kite uses:
    - **Stabilization Windows:** Decisions are based on long-term trends (e.g., 95th percentile over 24h).
    - **Hysteresis:** Changes are applied only if the delta exceeds a configurable threshold (e.g., >10%).
    - **Cooldowns:** Minimum time between scaling events.
    

## 3. Tech Stack
- **Language:** Go (Golang)
- **Framework:** Kubebuilder (Operator-SDK)
- **Data Source:** Prometheus HTTP API
- **Target:** Kubernetes Deployments / StatefulSets

## 4. Custom Resource Definition (CRD) Schema
The operator reconciles the `OptimizationTarget` kind