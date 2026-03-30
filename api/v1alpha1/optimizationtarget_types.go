package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HPABehavior defines how the operator handles workloads managed by a HorizontalPodAutoscaler.
//
// +kubebuilder:validation:Enum=Skip;Include
type HPABehavior string

const (
	// HPABehaviorSkip causes HPA-managed workloads to be omitted from recommendations.
	HPABehaviorSkip HPABehavior = "Skip"
	// HPABehaviorInclude includes HPA-managed workloads; the recommendation is computed
	// from per-replica metrics and annotated with HPA metadata.
	HPABehaviorInclude HPABehavior = "Include"
)

// TargetSelector identifies which workloads should be analysed.
type TargetSelector struct {
	// Namespaces is the list of namespaces to inspect.
	// When empty all non-system namespaces are included.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// ExcludeNamespaces lists namespaces that must be skipped even when they
	// match the Namespaces field or the "all namespaces" default.
	// +optional
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// LabelSelector restricts which workloads are considered.
	// When omitted all workloads of the requested kinds are included.
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// Kinds lists the workload resource kinds to optimise.
	// Supported values: Deployment, StatefulSet, DaemonSet.
	// Defaults to all three kinds when empty.
	// +optional
	Kinds []string `json:"kinds,omitempty"`
}

// RightsizingRules controls how recommendations are computed.
type RightsizingRules struct {
	// CPUPercentile is the percentile (1-100) of observed per-replica CPU
	// samples used as the baseline for the CPU request recommendation.
	// Defaults to 95.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	CPUPercentile int32 `json:"cpuPercentile,omitempty"`

	// MemoryPercentile is the percentile (1-100) of observed per-replica memory
	// samples used as the baseline for the memory request recommendation.
	// Defaults to 100 (i.e. the observed maximum).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	MemoryPercentile int32 `json:"memoryPercentile,omitempty"`

	// CPUSafetyMarginPercent is added on top of the CPUPercentile baseline.
	// A value of 15 means the recommendation is 115 % of the baseline.
	// Defaults to 15.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=200
	// +optional
	CPUSafetyMarginPercent int32 `json:"cpuSafetyMarginPercent,omitempty"`

	// MemorySafetyMarginPercent is added on top of the MemoryPercentile baseline.
	// Defaults to 15.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=200
	// +optional
	MemorySafetyMarginPercent int32 `json:"memorySafetyMarginPercent,omitempty"`

	// MinCPURequest is the floor for any CPU request recommendation.
	// Defaults to "10m".
	// +optional
	MinCPURequest resource.Quantity `json:"minCPURequest,omitempty"`

	// MinMemoryRequest is the floor for any memory request recommendation.
	// Defaults to "32Mi".
	// +optional
	MinMemoryRequest resource.Quantity `json:"minMemoryRequest,omitempty"`

	// CPULimitRatio, when set, defines the CPU limit as a multiple of the
	// recommended CPU request (e.g. "2.0" → limit = 2 × request).
	// When omitted the CPU limit is removed from the recommendation.
	// +optional
	CPULimitRatio *string `json:"cpuLimitRatio,omitempty"`

	// MemoryLimitRatio, when set, defines the memory limit as a multiple of the
	// recommended memory request (e.g. "1.3" → limit = 1.3 × request).
	// When omitted the memory limit equals the request (ratio of 1.0).
	// +optional
	MemoryLimitRatio *string `json:"memoryLimitRatio,omitempty"`

	// HistoryWindow is the lookback duration used when computing recommendations.
	// Only data points within this window are considered.
	// Defaults to 24h.
	// +optional
	HistoryWindow *metav1.Duration `json:"historyWindow,omitempty"`

	// HPABehavior controls how HPA-managed workloads are handled.
	// Defaults to Include.
	// +optional
	HPABehavior HPABehavior `json:"hpaBehavior,omitempty"`
}

// GitOpsConfig describes where and how to open pull requests with the
// recommended resource changes.
type GitOpsConfig struct {
	// Provider is the Git hosting provider: "github" or "gitlab".
	// +kubebuilder:validation:Enum=github;gitlab
	Provider string `json:"provider"`

	// RepoURL is the HTTPS URL of the GitOps repository
	// (e.g. "https://github.com/my-org/my-infra").
	RepoURL string `json:"repoURL"`

	// BaseBranch is the branch that pull requests will target.
	// Defaults to "main".
	// +optional
	BaseBranch string `json:"baseBranch,omitempty"`

	// SecretRef references a Secret in the operator's namespace that must
	// contain a key named "token" holding a personal-access token with
	// repository write permissions.
	SecretRef corev1.LocalObjectReference `json:"secretRef"`

	// PathTemplate is a Go text/template expression that resolves to the
	// relative path of the manifest file inside the repository.
	// Available variables: .Namespace  .Name  .Kind
	// Example: "clusters/production/{{.Namespace}}/{{.Name}}.yaml"
	PathTemplate string `json:"pathTemplate"`

	// PRTitleTemplate is a Go template for the pull-request title.
	// Available variables: .Namespace  .Name  .Kind
	// +optional
	PRTitleTemplate string `json:"prTitleTemplate,omitempty"`

	// PRBodyTemplate is a Go template for the pull-request body (Markdown).
	// Available variables: .Namespace  .Name  .Kind  .Recommendations
	// +optional
	PRBodyTemplate string `json:"prBodyTemplate,omitempty"`

	// PRLabels are labels attached to every pull request opened by Kite.
	// +optional
	PRLabels []string `json:"prLabels,omitempty"`

	// AutoMerge, when true, instructs the provider to merge the PR
	// immediately after creation (requires sufficient repo permissions).
	// +optional
	AutoMerge bool `json:"autoMerge,omitempty"`

	// CommitMessageTemplate is a Go template for the commit message.
	// +optional
	CommitMessageTemplate string `json:"commitMessageTemplate,omitempty"`

	// Reviewers lists GitHub usernames or GitLab user IDs to request a
	// review from.
	// +optional
	Reviewers []string `json:"reviewers,omitempty"`
}

// OptimizationTargetSpec defines the desired configuration for a rightsizing
// analysis run.
type OptimizationTargetSpec struct {
	// Target defines which workloads are in scope.
	// +kubebuilder:validation:Required
	Target TargetSelector `json:"target"`

	// Schedule is a standard five-field cron expression describing how
	// frequently a full analysis should be run.
	// Example: "0 2 * * *" (every day at 02:00 UTC).
	// +kubebuilder:validation:Required
	Schedule string `json:"schedule"`

	// ScrapeInterval controls how often Kite queries the metrics-server to
	// record a new data point for each workload.
	// Defaults to 5 minutes.
	// +optional
	ScrapeInterval *metav1.Duration `json:"scrapeInterval,omitempty"`

	// Rules controls how the rightsizing algorithm computes recommendations.
	// +optional
	Rules RightsizingRules `json:"rules,omitempty"`

	// GitOps, when provided, enables automated pull-request creation in the
	// specified repository.  When omitted Kite operates in report-only mode.
	// +optional
	GitOps *GitOpsConfig `json:"gitOps,omitempty"`

	// DryRun, when true, causes the operator to compute recommendations and
	// populate the status but skip PR creation even if GitOps is configured.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

// ContainerRecommendation holds the rightsizing recommendation for a single
// container within a workload.
type ContainerRecommendation struct {
	// Name is the container name.
	Name string `json:"name"`

	// CurrentCPURequest is the current CPU request as a resource string.
	CurrentCPURequest string `json:"currentCPURequest"`

	// CurrentCPULimit is the current CPU limit (empty when no limit is set).
	// +optional
	CurrentCPULimit string `json:"currentCPULimit,omitempty"`

	// CurrentMemoryRequest is the current memory request.
	CurrentMemoryRequest string `json:"currentMemoryRequest"`

	// CurrentMemoryLimit is the current memory limit (empty when no limit is set).
	// +optional
	CurrentMemoryLimit string `json:"currentMemoryLimit,omitempty"`

	// RecommendedCPURequest is the suggested CPU request.
	RecommendedCPURequest string `json:"recommendedCPURequest"`

	// RecommendedCPULimit is the suggested CPU limit (empty → remove the limit).
	// +optional
	RecommendedCPULimit string `json:"recommendedCPULimit,omitempty"`

	// RecommendedMemoryRequest is the suggested memory request.
	RecommendedMemoryRequest string `json:"recommendedMemoryRequest"`

	// RecommendedMemoryLimit is the suggested memory limit.
	// +optional
	RecommendedMemoryLimit string `json:"recommendedMemoryLimit,omitempty"`

	// CPURequestDiffPercent is the relative change in CPU request
	// (positive = increase, negative = decrease).
	CPURequestDiffPercent float64 `json:"cpuRequestDiffPercent"`

	// MemoryRequestDiffPercent is the relative change in memory request.
	MemoryRequestDiffPercent float64 `json:"memoryRequestDiffPercent"`

	// Confidence reflects the quality of the underlying data.
	// Possible values: none (< 1 sample), low (< 10), medium (< 50), high (≥ 50).
	Confidence string `json:"confidence"`
}

// WorkloadRecommendation aggregates per-container recommendations for a
// single workload.
type WorkloadRecommendation struct {
	// Namespace of the workload.
	Namespace string `json:"namespace"`

	// Name of the workload.
	Name string `json:"name"`

	// Kind of the workload (Deployment, StatefulSet, DaemonSet).
	Kind string `json:"kind"`

	// CurrentReplicas is the observed replica count at analysis time.
	// +optional
	CurrentReplicas int32 `json:"currentReplicas,omitempty"`

	// HPAManaged is true when a HorizontalPodAutoscaler targets this workload.
	// +optional
	HPAManaged bool `json:"hpaManaged,omitempty"`

	// HPAMinReplicas is the minimum replica count configured on the HPA.
	// +optional
	HPAMinReplicas *int32 `json:"hpaMinReplicas,omitempty"`

	// HPAMaxReplicas is the maximum replica count configured on the HPA.
	// +optional
	HPAMaxReplicas *int32 `json:"hpaMaxReplicas,omitempty"`

	// DataPoints is the number of metrics samples that contributed to this
	// recommendation.
	DataPoints int32 `json:"dataPoints"`

	// Containers holds the per-container recommendations.
	// +optional
	Containers []ContainerRecommendation `json:"containers,omitempty"`

	// PRUrl is the URL of the pull request that was opened (if any).
	// +optional
	PRUrl string `json:"prUrl,omitempty"`

	// PRStatus reflects the current state of the pull request.
	// +optional
	PRStatus string `json:"prStatus,omitempty"`

	// GeneratedAt is the timestamp of this recommendation.
	GeneratedAt metav1.Time `json:"generatedAt"`

	// Skipped is true when the workload was intentionally excluded from
	// analysis (e.g. HPABehavior=Skip).
	// +optional
	Skipped bool `json:"skipped,omitempty"`

	// SkipReason describes why the workload was skipped.
	// +optional
	SkipReason string `json:"skipReason,omitempty"`
}

// OptimizationTargetStatus reflects the observed state of an OptimizationTarget.
type OptimizationTargetStatus struct {
	// Conditions represent the current lifecycle phase of the OptimizationTarget.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation that was last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastAnalysisTime is when the most recent analysis run completed.
	// +optional
	LastAnalysisTime *metav1.Time `json:"lastAnalysisTime,omitempty"`

	// NextAnalysisTime is when the next scheduled analysis will run.
	// +optional
	NextAnalysisTime *metav1.Time `json:"nextAnalysisTime,omitempty"`

	// LastScrapeTime is when metrics were last collected from the metrics-server.
	// +optional
	LastScrapeTime *metav1.Time `json:"lastScrapeTime,omitempty"`

	// TotalWorkloads is the total number of workloads analysed in the last run.
	// +optional
	TotalWorkloads int32 `json:"totalWorkloads,omitempty"`

	// WorkloadsWithPR is the number of workloads for which a PR was opened.
	// +optional
	WorkloadsWithPR int32 `json:"workloadsWithPR,omitempty"`

	// Recommendations is the full list of rightsizing recommendations produced
	// by the last analysis run.
	// +optional
	Recommendations []WorkloadRecommendation `json:"recommendations,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ot
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Last Analysis",type=date,JSONPath=`.status.lastAnalysisTime`
// +kubebuilder:printcolumn:name="Workloads",type=integer,JSONPath=`.status.totalWorkloads`
// +kubebuilder:printcolumn:name="PRs Opened",type=integer,JSONPath=`.status.workloadsWithPR`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OptimizationTarget is the Schema for the optimizationtargets API.
// It declares which workloads Kite should rightsize, the algorithm parameters,
// and – optionally – how to open pull requests in a GitOps repository.
type OptimizationTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OptimizationTargetSpec   `json:"spec,omitempty"`
	Status OptimizationTargetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OptimizationTargetList contains a list of OptimizationTarget.
type OptimizationTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OptimizationTarget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OptimizationTarget{}, &OptimizationTargetList{})
}
