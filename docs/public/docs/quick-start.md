# Quick Start

This page walks through a complete end-to-end example in under 5 minutes.

## 1. Install Kite

```bash
kubectl apply -k https://github.com/graz-dev/kite/config/default/?ref=main
kubectl -n kite-system rollout status deploy/kite-controller-manager
```

## 2. Create a report-only target

```bash
kubectl apply -f - <<'EOF'
apiVersion: optimization.kite.dev/v1alpha1
kind: OptimizationTarget
metadata:
  name: quick-start
spec:
  target:
    namespaces: [default]
  schedule: "*/10 * * * *"   # every 10 minutes for demo purposes
  scrapeInterval: 1m
  rules:
    historyWindow: 10m
  dryRun: true
EOF
```

## 3. Wait for the first analysis

```bash
# Watch the status update
kubectl get ot quick-start -w
```

After the first analysis you'll see:

```
NAME          SCHEDULE          LAST ANALYSIS          WORKLOADS   PRS OPENED
quick-start   */10 * * * *      2025-03-30T10:10:00Z   5           0
```

## 4. Inspect recommendations

```bash
kubectl get ot quick-start -o jsonpath='{.status.recommendations}' | jq .
```

## 5. Enable GitOps (optional)

```bash
# Create the GitHub token secret
kubectl create secret generic github-token \
  --from-literal=token=ghp_YOUR_TOKEN \
  -n kite-system

# Update the target
kubectl patch ot quick-start --type=merge -p '{
  "spec": {
    "dryRun": false,
    "gitOps": {
      "provider": "github",
      "repoURL": "https://github.com/my-org/my-infra",
      "secretRef": {"name": "github-token"},
      "pathTemplate": "apps/{{.Namespace}}/{{.Name}}/deployment.yaml"
    }
  }
}'
```

On the next analysis run, Kite will open a pull request for each workload that
has a meaningful recommendation.
