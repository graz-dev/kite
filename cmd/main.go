// Package main is the entrypoint for the Kite operator.
//
// The operator is built with controller-runtime and manages two CRDs:
//   - OptimizationTarget  (cluster-scoped)
//   - MetricsHistory      (namespace-scoped, stored in the operator's namespace)
package main

import (
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g., Azure, GCP, OIDC).
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	optimizationv1alpha1 "github.com/graz-dev/kite/api/v1alpha1"
	"github.com/graz-dev/kite/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(autoscalingv2.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(optimizationv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		enableLeaderElection bool
		probeAddr            string
		operatorNamespace    string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"The address the Prometheus metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"The address the health probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&operatorNamespace, "operator-namespace", "",
		"Namespace where the operator is deployed. "+
			"MetricsHistory CRDs are stored here. "+
			"Defaults to the POD_NAMESPACE environment variable.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Resolve operator namespace.
	if operatorNamespace == "" {
		operatorNamespace = os.Getenv("POD_NAMESPACE")
	}
	if operatorNamespace == "" {
		operatorNamespace = "kite-system"
	}

	setupLog.Info("Starting Kite operator",
		"operator-namespace", operatorNamespace,
		"version", version(),
	)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kite-leader-election.optimization.kite.dev",
	})
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	// Build the metrics-server client using the same kubeconfig as the manager.
	metricsClientset, err := metricsclient.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "Unable to create metrics client")
		os.Exit(1)
	}

	if err = (&controller.OptimizationTargetReconciler{
		Client:            mgr.GetClient(),
		MetricsClient:     metricsClientset,
		OperatorNamespace: operatorNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create controller", "controller", "OptimizationTarget")
		os.Exit(1)
	}

	// Health and readiness probes.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}

// version returns a human-readable version string.
// In production builds this is overridden via -ldflags.
func version() string {
	v := os.Getenv("KITE_VERSION")
	if v != "" {
		return v
	}
	return fmt.Sprintf("dev-%s", "local")
}
