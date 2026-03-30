package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadRef identifies a specific Kubernetes workload.
type WorkloadRef struct {
	// Namespace of the workload.
	Namespace string `json:"namespace"`

	// Name of the workload.
	Name string `json:"name"`

	// Kind of the workload (Deployment, StatefulSet, DaemonSet).
	Kind string `json:"kind"`
}

// ContainerMetrics holds the resource usage of a single container at one
// point in time.  Values are the average over all running replicas at that
// instant (i.e. per-replica figures).
type ContainerMetrics struct {
	// Name is the container name.
	Name string `json:"name"`

	// CPUMilliCores is the CPU usage in milli-cores (1 core = 1000 m).
	CPUMilliCores int64 `json:"cpuMilliCores"`

	// MemoryBytes is the working-set memory usage in bytes.
	MemoryBytes int64 `json:"memoryBytes"`
}

// MetricsDataPoint is one observation of a workload's resource usage.
type MetricsDataPoint struct {
	// Timestamp is when the observation was recorded.
	Timestamp metav1.Time `json:"timestamp"`

	// ReplicaCount is the number of running pods at observation time.
	ReplicaCount int32 `json:"replicaCount"`

	// Containers holds per-container usage figures.
	Containers []ContainerMetrics `json:"containers"`
}

// MetricsHistorySpec stores accumulated resource-usage observations for a
// single workload.
type MetricsHistorySpec struct {
	// WorkloadRef identifies the workload these metrics belong to.
	WorkloadRef WorkloadRef `json:"workloadRef"`

	// MaxDataPoints is the maximum number of data points to retain.
	// Older points are pruned automatically.
	// Defaults to 2016 (≈ 7 days at 5-minute intervals).
	// +optional
	MaxDataPoints int32 `json:"maxDataPoints,omitempty"`

	// DataPoints is the time-ordered list of observations.
	// +optional
	DataPoints []MetricsDataPoint `json:"dataPoints,omitempty"`
}

// MetricsHistoryStatus is currently unused; reserved for future conditions.
type MetricsHistoryStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=mh
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workloadRef.name`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.workloadRef.namespace`
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.workloadRef.kind`
// +kubebuilder:printcolumn:name="Points",type=integer,JSONPath=`.spec.maxDataPoints`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MetricsHistory is the Schema for the metricshistories API.
// The Kite operator creates and maintains one MetricsHistory object per
// workload, storing a rolling window of metrics-server observations that
// are later used by the rightsizing algorithm.
type MetricsHistory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MetricsHistorySpec   `json:"spec,omitempty"`
	Status MetricsHistoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MetricsHistoryList contains a list of MetricsHistory.
type MetricsHistoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MetricsHistory `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MetricsHistory{}, &MetricsHistoryList{})
}
