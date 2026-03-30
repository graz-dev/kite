// Package controller implements the OptimizationTarget reconciler.
//
// # Reconcile loop
//
// The controller is entirely event-driven: it uses ctrl.Result{RequeueAfter}
// to wake itself up at exactly the right time rather than running background
// goroutines.  On every reconcile iteration it performs two independent checks:
//
//  1. Metrics scrape – if now ≥ lastScrapeTime + scrapeInterval, query the
//     metrics-server for all workloads that are in scope and persist the
//     observations into MetricsHistory CRDs.
//
//  2. Analysis – if now ≥ nextAnalysisTime (derived from the cron schedule),
//     compute rightsizing recommendations using the accumulated history and
//     (optionally) open pull requests in the configured GitOps repository.
//
// After both checks the controller calculates the earlier of the two next
// trigger times and requeues accordingly.
package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	optimizationv1alpha1 "github.com/graz-dev/kite/api/v1alpha1"
	"github.com/graz-dev/kite/internal/algorithm"
	"github.com/graz-dev/kite/internal/gitops"
	"github.com/graz-dev/kite/internal/metrics"
)

const (
	// conditionTypeReady is the primary condition type used on OptimizationTarget.
	conditionTypeReady = "Ready"

	// finalizerName is added to OptimizationTarget objects managed by Kite.
	finalizerName = "optimization.kite.dev/finalizer"

	// defaultBaseBranch is used when GitOpsConfig.BaseBranch is empty.
	defaultBaseBranch = "main"
)

// OptimizationTargetReconciler reconciles OptimizationTarget objects.
//
// +kubebuilder:rbac:groups=optimization.kite.dev,resources=optimizationtargets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=optimization.kite.dev,resources=optimizationtargets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=optimization.kite.dev,resources=optimizationtargets/finalizers,verbs=update
// +kubebuilder:rbac:groups=optimization.kite.dev,resources=metricshistories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods;namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list
type OptimizationTargetReconciler struct {
	client.Client
	MetricsClient     metricsclient.Interface
	OperatorNamespace string
}

// SetupWithManager registers the controller with the manager.
func (r *OptimizationTargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&optimizationv1alpha1.OptimizationTarget{}).
		Complete(r)
}

// Reconcile is the main reconcile loop entry point.
func (r *OptimizationTargetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("optimizationtarget", req.Name)

	target := &optimizationv1alpha1.OptimizationTarget{}
	if err := r.Get(ctx, req.NamespacedName, target); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion.
	if !target.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, target)
	}

	// Ensure finalizer is present.
	if !containsString(target.Finalizers, finalizerName) {
		target.Finalizers = append(target.Finalizers, finalizerName)
		if err := r.Update(ctx, target); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Parse and validate the cron schedule.
	cronSchedule, err := parseCron(target.Spec.Schedule)
	if err != nil {
		return r.setFailedCondition(ctx, target,
			fmt.Sprintf("invalid schedule %q: %v", target.Spec.Schedule, err))
	}

	now := time.Now()

	// --- Determine trigger times -------------------------------------------

	scrapeInterval := metrics.DefaultScrapeInterval
	if target.Spec.ScrapeInterval != nil {
		scrapeInterval = target.Spec.ScrapeInterval.Duration
	}

	var lastScrape time.Time
	if target.Status.LastScrapeTime != nil {
		lastScrape = target.Status.LastScrapeTime.Time
	}
	nextScrape := lastScrape.Add(scrapeInterval)

	var lastAnalysis time.Time
	if target.Status.LastAnalysisTime != nil {
		lastAnalysis = target.Status.LastAnalysisTime.Time
	}
	nextAnalysis := cronSchedule.Next(lastAnalysis)
	if lastAnalysis.IsZero() {
		// First run: scrape immediately and analyse in one minute to allow
		// at least one data point to be collected.
		nextScrape = now
		nextAnalysis = now.Add(1 * time.Minute)
	}

	// --- Metrics scrape ----------------------------------------------------

	collector := &metrics.Collector{
		Client:            r.Client,
		MetricsClient:     r.MetricsClient,
		OperatorNamespace: r.OperatorNamespace,
	}

	var workloadSummaries []metrics.WorkloadSummary
	if !now.Before(nextScrape) {
		logger.Info("Scraping metrics-server")
		ws, err := collector.ScrapeAndPersist(ctx, target)
		if err != nil {
			logger.Error(err, "Metrics scrape failed; will retry on next schedule")
		} else {
			workloadSummaries = ws
		}
		now2 := metav1.Now()
		target.Status.LastScrapeTime = &now2
		nextScrape = now.Add(scrapeInterval)
	}

	// --- Analysis -----------------------------------------------------------

	if !now.Before(nextAnalysis) {
		logger.Info("Running rightsizing analysis")

		// If we just scraped, workloadSummaries is already populated.
		// Otherwise re-discover workloads from the cluster.
		if workloadSummaries == nil {
			ws, err := collector.ScrapeAndPersist(ctx, target)
			if err != nil {
				logger.Error(err, "Could not discover workloads for analysis")
			} else {
				workloadSummaries = ws
			}
		}

		recs, prCount, err := r.runAnalysis(ctx, target, workloadSummaries, collector)
		if err != nil {
			logger.Error(err, "Analysis failed")
		} else {
			now2 := metav1.Now()
			target.Status.LastAnalysisTime = &now2
			target.Status.Recommendations = recs
			target.Status.TotalWorkloads = int32(len(recs))
			target.Status.WorkloadsWithPR = int32(prCount)
		}
		nextAnalysis = cronSchedule.Next(now)
	}

	// Store the next analysis time so it appears in status.
	t := metav1.NewTime(nextAnalysis)
	target.Status.NextAnalysisTime = &t
	target.Status.ObservedGeneration = target.Generation

	setCondition(&target.Status, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "OptimizationTarget is healthy and processing.",
		ObservedGeneration: target.Generation,
	})

	if err := r.Status().Update(ctx, target); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue at whichever trigger comes first.
	requeueAt := minTime(nextScrape, nextAnalysis)
	delay := time.Until(requeueAt)
	if delay < 0 {
		delay = 0
	}
	logger.Info("Requeuing", "in", delay.Round(time.Second).String())
	return ctrl.Result{RequeueAfter: delay}, nil
}

// runAnalysis computes recommendations for all workloads and optionally
// creates pull requests.  It returns the slice of recommendations plus the
// number of workloads for which a PR was opened.
func (r *OptimizationTargetReconciler) runAnalysis(
	ctx context.Context,
	target *optimizationv1alpha1.OptimizationTarget,
	summaries []metrics.WorkloadSummary,
	collector *metrics.Collector,
) ([]optimizationv1alpha1.WorkloadRecommendation, int, error) {
	logger := log.FromContext(ctx)

	var recommendations []optimizationv1alpha1.WorkloadRecommendation
	prCount := 0

	// Determine the GitOps provider once (nil when GitOps is not configured).
	var gitopsProvider gitops.Provider
	var gitopsToken string
	if target.Spec.GitOps != nil && !target.Spec.DryRun {
		token, err := r.readGitOpsToken(ctx, target)
		if err != nil {
			logger.Error(err, "Cannot read GitOps token; proceeding without PR creation")
		} else {
			gitopsToken = token
			gitopsProvider = newGitOpsProvider(target.Spec.GitOps.Provider)
		}
	}

	for _, ws := range summaries {
		history, err := collector.GetHistory(ctx, ws.Namespace, ws.Name, ws.Kind)
		if err != nil {
			logger.Error(err, "Cannot load MetricsHistory", "workload", ws.Name)
		}

		containers, err := r.discoverContainers(ctx, ws.Namespace, ws.Name, ws.Kind)
		if err != nil {
			logger.Error(err, "Cannot discover containers", "workload", ws.Name)
			continue
		}

		input := algorithm.WorkloadInput{
			Namespace:       ws.Namespace,
			Name:            ws.Name,
			Kind:            ws.Kind,
			CurrentReplicas: ws.CurrentReplicas,
			HPAManaged:      ws.HPAManaged,
			HPAMinReplicas:  ws.HPAMinReplicas,
			HPAMaxReplicas:  ws.HPAMaxReplicas,
			Containers:      containers,
			History:         history,
		}

		rec := algorithm.Compute(input, target.Spec.Rules)

		wrec := buildWorkloadRecommendation(ws, rec, input.Containers)

		// GitOps: open a PR if configured.
		if gitopsProvider != nil && !rec.Skipped && len(rec.Containers) > 0 {
			prURL, err := r.createPR(ctx, target, ws, rec.Containers, gitopsToken, gitopsProvider)
			if err != nil {
				logger.Error(err, "Failed to create PR", "workload", ws.Name)
				wrec.PRStatus = "error: " + err.Error()
			} else if prURL != "" {
				wrec.PRUrl = prURL
				wrec.PRStatus = "open"
				prCount++
			}
		}

		recommendations = append(recommendations, wrec)
	}

	return recommendations, prCount, nil
}

// discoverContainers returns the ContainerInput slice for a given workload by
// reading its current resource requests/limits from the cluster.
func (r *OptimizationTargetReconciler) discoverContainers(
	ctx context.Context,
	namespace, name, kind string,
) ([]algorithm.ContainerInput, error) {
	var podTemplateContainers []corev1.Container

	switch kind {
	case "Deployment":
		d := &appsv1.Deployment{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, d); err != nil {
			return nil, err
		}
		podTemplateContainers = d.Spec.Template.Spec.Containers

	case "StatefulSet":
		s := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, s); err != nil {
			return nil, err
		}
		podTemplateContainers = s.Spec.Template.Spec.Containers

	case "DaemonSet":
		d := &appsv1.DaemonSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, d); err != nil {
			return nil, err
		}
		podTemplateContainers = d.Spec.Template.Spec.Containers

	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}

	inputs := make([]algorithm.ContainerInput, 0, len(podTemplateContainers))
	for _, c := range podTemplateContainers {
		ci := algorithm.ContainerInput{Name: c.Name}

		if req := c.Resources.Requests; req != nil {
			if v, ok := req[corev1.ResourceCPU]; ok {
				ci.CurrentCPURequest = v
			}
			if v, ok := req[corev1.ResourceMemory]; ok {
				ci.CurrentMemoryRequest = v
			}
		}
		if lim := c.Resources.Limits; lim != nil {
			if v, ok := lim[corev1.ResourceCPU]; ok {
				tmp := v
				ci.CurrentCPULimit = &tmp
			}
			if v, ok := lim[corev1.ResourceMemory]; ok {
				tmp := v
				ci.CurrentMemoryLimit = &tmp
			}
		}
		inputs = append(inputs, ci)
	}
	return inputs, nil
}

// createPR renders templates and delegates to the appropriate git provider.
func (r *OptimizationTargetReconciler) createPR(
	ctx context.Context,
	target *optimizationv1alpha1.OptimizationTarget,
	ws metrics.WorkloadSummary,
	containerRecs []algorithm.ContainerRec,
	token string,
	provider gitops.Provider,
) (string, error) {
	gitOpsCfg := target.Spec.GitOps
	vars := gitops.TemplateVars{
		Namespace:       ws.Namespace,
		Name:            ws.Name,
		Kind:            ws.Kind,
		Recommendations: containerRecs,
	}

	// Resolve file path.
	filePath, err := gitops.RenderTemplate(gitOpsCfg.PathTemplate, vars)
	if err != nil {
		return "", fmt.Errorf("rendering path template: %w", err)
	}

	// Resolve PR title.
	title, err := gitops.RenderTemplate(gitOpsCfg.PRTitleTemplate, vars)
	if err != nil || title == "" {
		title = gitops.DefaultPRTitle(vars)
	}

	// Resolve PR body.
	body, err := gitops.RenderTemplate(gitOpsCfg.PRBodyTemplate, vars)
	if err != nil || body == "" {
		body = gitops.DefaultPRBody(vars)
	}

	// Resolve commit message.
	commitMsg, err := gitops.RenderTemplate(gitOpsCfg.CommitMessageTemplate, vars)
	if err != nil || commitMsg == "" {
		commitMsg = gitops.DefaultCommitMessage(vars)
	}

	baseBranch := gitOpsCfg.BaseBranch
	if baseBranch == "" {
		baseBranch = defaultBaseBranch
	}

	// Read the existing manifest to patch it.
	var existingContent []byte
	switch gitOpsCfg.Provider {
	case "github":
		ghp := &gitops.GitHubProvider{}
		existingContent, err = ghp.ReadFileFromRepo(ctx, gitOpsCfg.RepoURL, token, baseBranch, filePath)
	case "gitlab":
		glp := &gitops.GitLabProvider{}
		existingContent, err = glp.ReadFileFromRepo(ctx, gitOpsCfg.RepoURL, token, baseBranch, filePath)
	}
	if err != nil {
		return "", fmt.Errorf("reading manifest from repo: %w", err)
	}

	// Patch the manifest with our recommendations.
	patched, err := gitops.UpdateResourcesInManifest(existingContent, containerRecs)
	if err != nil {
		return "", fmt.Errorf("patching manifest: %w", err)
	}

	branchName := gitops.BranchName(ws.Namespace, ws.Name, ws.Kind)

	prReq := gitops.PRRequest{
		RepoURL:    gitOpsCfg.RepoURL,
		Token:      token,
		BaseBranch: baseBranch,
		BranchName: branchName,
		Title:      title,
		Body:       body,
		Labels:     gitOpsCfg.PRLabels,
		Reviewers:  gitOpsCfg.Reviewers,
		AutoMerge:  gitOpsCfg.AutoMerge,
		CommitMsg:  commitMsg,
		Files: []gitops.FileChange{
			{Path: filePath, Content: patched},
		},
	}

	result, err := provider.CreatePR(ctx, prReq)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

// readGitOpsToken fetches the personal access token from the referenced Secret.
func (r *OptimizationTargetReconciler) readGitOpsToken(
	ctx context.Context,
	target *optimizationv1alpha1.OptimizationTarget,
) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: r.OperatorNamespace,
		Name:      target.Spec.GitOps.SecretRef.Name,
	}, secret); err != nil {
		return "", fmt.Errorf("reading secret %s: %w", target.Spec.GitOps.SecretRef.Name, err)
	}
	token, ok := secret.Data["token"]
	if !ok {
		return "", fmt.Errorf("secret %s does not contain key 'token'", target.Spec.GitOps.SecretRef.Name)
	}
	return string(token), nil
}

// handleDeletion removes the finalizer and cleans up if necessary.
func (r *OptimizationTargetReconciler) handleDeletion(
	ctx context.Context,
	target *optimizationv1alpha1.OptimizationTarget,
) (ctrl.Result, error) {
	if containsString(target.Finalizers, finalizerName) {
		// Nothing to clean up externally (MetricsHistory objects are left
		// intentionally – they may be useful for future analysis and are
		// clearly labelled as managed by kite).
		target.Finalizers = removeString(target.Finalizers, finalizerName)
		if err := r.Update(ctx, target); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// setFailedCondition marks the OptimizationTarget as not ready and returns a
// Result that stops reconciliation until the object is modified.
func (r *OptimizationTargetReconciler) setFailedCondition(
	ctx context.Context,
	target *optimizationv1alpha1.OptimizationTarget,
	msg string,
) (ctrl.Result, error) {
	setCondition(&target.Status, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "ConfigurationError",
		Message:            msg,
		ObservedGeneration: target.Generation,
	})
	_ = r.Status().Update(ctx, target)
	return ctrl.Result{}, nil
}

// --- builder helpers ---------------------------------------------------------

func buildWorkloadRecommendation(
	ws metrics.WorkloadSummary,
	rec algorithm.Recommendation,
	containers []algorithm.ContainerInput,
) optimizationv1alpha1.WorkloadRecommendation {
	wr := optimizationv1alpha1.WorkloadRecommendation{
		Namespace:       ws.Namespace,
		Name:            ws.Name,
		Kind:            ws.Kind,
		CurrentReplicas: ws.CurrentReplicas,
		HPAManaged:      ws.HPAManaged,
		HPAMinReplicas:  ws.HPAMinReplicas,
		HPAMaxReplicas:  ws.HPAMaxReplicas,
		DataPoints:      rec.DataPoints,
		GeneratedAt:     metav1.Now(),
		Skipped:         rec.Skipped,
		SkipReason:      rec.SkipReason,
	}

	// Build a lookup for current resources.
	currentMap := make(map[string]algorithm.ContainerInput, len(containers))
	for _, ci := range containers {
		currentMap[ci.Name] = ci
	}

	for _, cr := range rec.Containers {
		ci := currentMap[cr.Name]
		crec := optimizationv1alpha1.ContainerRecommendation{
			Name:                     cr.Name,
			CurrentCPURequest:        quantityStr(ci.CurrentCPURequest),
			CurrentMemoryRequest:     quantityStr(ci.CurrentMemoryRequest),
			RecommendedCPURequest:    cr.RecommendedCPURequest.String(),
			RecommendedMemoryRequest: cr.RecommendedMemoryRequest.String(),
			CPURequestDiffPercent:    cr.CPURequestDiffPercent,
			MemoryRequestDiffPercent: cr.MemoryRequestDiffPercent,
			Confidence:               rec.Confidence,
		}
		if ci.CurrentCPULimit != nil {
			crec.CurrentCPULimit = ci.CurrentCPULimit.String()
		}
		if ci.CurrentMemoryLimit != nil {
			crec.CurrentMemoryLimit = ci.CurrentMemoryLimit.String()
		}
		if cr.RecommendedCPULimit != nil {
			crec.RecommendedCPULimit = cr.RecommendedCPULimit.String()
		}
		if cr.RecommendedMemoryLimit != nil {
			crec.RecommendedMemoryLimit = cr.RecommendedMemoryLimit.String()
		}
		wr.Containers = append(wr.Containers, crec)
	}

	return wr
}

func quantityStr(q resource.Quantity) string {
	if q.IsZero() {
		return "0"
	}
	return q.String()
}

// --- condition helpers -------------------------------------------------------

func setCondition(status *optimizationv1alpha1.OptimizationTargetStatus, cond metav1.Condition) {
	cond.LastTransitionTime = metav1.Now()
	for i, c := range status.Conditions {
		if c.Type == cond.Type {
			if c.Status == cond.Status {
				cond.LastTransitionTime = c.LastTransitionTime
			}
			status.Conditions[i] = cond
			return
		}
	}
	status.Conditions = append(status.Conditions, cond)
}

// --- cron helpers ------------------------------------------------------------

func parseCron(expr string) (cron.Schedule, error) {
	parser := cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)
	return parser.Parse(expr)
}

// --- gitops provider factory -------------------------------------------------

func newGitOpsProvider(provider string) gitops.Provider {
	switch provider {
	case "gitlab":
		return &gitops.GitLabProvider{}
	default:
		return &gitops.GitHubProvider{}
	}
}

// --- misc helpers ------------------------------------------------------------

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	out := slice[:0]
	for _, item := range slice {
		if item != s {
			out = append(out, item)
		}
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
