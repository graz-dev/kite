# FAQ

## General

**Q: How is Kite different from KRR?**

KRR is a CLI tool that queries Prometheus and prints recommendations to stdout.
Kite is an operator that lives in your cluster, uses the metrics-server (no
Prometheus needed), stores its own metrics history, and can open GitOps pull
requests automatically.

---

**Q: Do I need Prometheus?**

No. Kite uses the Kubernetes metrics-server API (`metrics.k8s.io/v1beta1`),
which is bundled with most managed Kubernetes offerings (EKS, GKE, AKS) and
can be self-installed in minutes.

---

**Q: Can I use Kite alongside VPA (Vertical Pod Autoscaler)?**

Yes, but be careful: if VPA is also modifying resource requests on the same
workloads, the two systems may conflict.  Consider using Kite in `dryRun:
true` mode when VPA is active.

---

## Algorithm

**Q: How much historical data do I need for a good recommendation?**

At least one full business cycle — typically 24–48 hours for always-on
services.  For workloads with weekly patterns (batch jobs, weekend spikes),
7 days is better.  Use the `historyWindow` and check the `confidence` field
in the status.

---

**Q: Why is the CPU percentile 95 but memory is 100 (maximum)?**

Memory OOM kills are disruptive; a single spike can crash a pod.  Taking the
maximum for memory ensures the pod can survive its worst observed day.  CPU
throttling is recoverable, so the 95th percentile is a good balance between
efficiency and performance.

---

**Q: My workload has spiky CPU usage — what settings should I use?**

```yaml
rules:
  cpuPercentile: 99          # capture more of the distribution
  cpuSafetyMarginPercent: 25 # extra headroom
  cpuLimitRatio: "3.0"       # allow bursting
```

---

## GitOps

**Q: What file formats does Kite support for patching?**

Plain Kubernetes YAML manifests for Deployment, StatefulSet, DaemonSet, Job,
and CronJob.  Helm `values.yaml` and Kustomize overlays are not yet supported.

---

**Q: Does Kite preserve YAML comments when patching?**

No. The manifest is parsed through JSON and serialised back to YAML, which
strips comments.  This is a known limitation.  If this is blocking, consider
using the `dryRun: true` mode and applying recommendations manually.

---

**Q: Can I target a self-hosted GitLab instance?**

Not through the CRD field yet — the `baseURL` field for GitLab is available in
the `GitLabProvider` struct but not yet exposed as a CRD field.  Contributions
are welcome!

---

## Operations

**Q: How do I force an immediate analysis?**

Add or update any annotation to trigger a reconcile:

```bash
kubectl annotate ot my-target kite.dev/force-analysis=$(date +%s) --overwrite
```

---

**Q: How do I delete MetricsHistory objects for a workload that no longer exists?**

```bash
kubectl delete mh -n kite-system \
  -l kite.dev/workload-namespace=production,kite.dev/workload-name=old-app
```

---

**Q: Does Kite support multiple clusters?**

One Kite installation manages one cluster.  For multi-cluster setups, deploy
Kite in each cluster.
