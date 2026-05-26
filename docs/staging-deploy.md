# Staging deploy runbook

Purpose: deploy `app-eai-agent-gateway` changes to the staging Kubernetes
environment and verify that the running pods use the intended image.

Audience: operators with GitHub Actions, GHCR, and GKE staging access.

## Scope

This runbook covers the Gateway API deployment and the async worker deployment
in the `eai-agent-gateway` Kubernetes namespace.

It does not deploy a new Vertex AI Reasoning Engine. `REASONING_ENGINE_ID` is a
runtime configuration value consumed by the Gateway; update it only when a new
Engine artifact has already been deployed and the Gateway should route traffic
to that Engine.

## What is automatic

Pushes to the `staging` branch trigger `.github/workflows/build-container.yaml`.
That workflow builds and pushes the container image to GHCR with two tags:

- `ghcr.io/prefeitura-rio/app-eai-agent-gateway:latest`
- `ghcr.io/prefeitura-rio/app-eai-agent-gateway:<commit_sha>`

The workflow does not run `kubectl apply`, does not update Kubernetes
Deployments, and does not restart pods. A Kubernetes rollout is still required
after the image build succeeds.

## Prerequisites

- `gh` authenticated with access to `prefeitura-rio/app-eai-agent-gateway`
- `kubectl` authenticated against staging
- Optional: Docker with `buildx` for registry digest inspection

Confirm the Kubernetes context before touching shared infrastructure:

```bash
kubectl config current-context
```

Expected staging context:

```text
gke_rj-superapp-staging_us-central1_application
```

Set common variables:

```bash
REPO="prefeitura-rio/app-eai-agent-gateway"
NS="eai-agent-gateway"
IMAGE_REPO="ghcr.io/prefeitura-rio/app-eai-agent-gateway"
```

## Deploy code changes

1. Confirm the latest successful `staging` build.

   ```bash
   gh -R "$REPO" run list --branch staging --limit 5
   ```

2. Read the run details and capture the commit SHA.

   ```bash
   RUN_ID="<github-actions-run-id>"

   gh -R "$REPO" run view "$RUN_ID" \
     --json status,conclusion,headSha,displayTitle,updatedAt

   HEAD_SHA="$(gh -R "$REPO" run view "$RUN_ID" --json headSha -q .headSha)"
   IMAGE="$IMAGE_REPO:$HEAD_SHA"
   ```

   Continue only when `status=completed` and `conclusion=success`.

3. Optional: inspect the pushed image.

   ```bash
   docker buildx imagetools inspect "$IMAGE" | sed -n '1,30p'
   ```

4. Deploy the exact commit image to both Deployments.

   ```bash
   kubectl set image deployment/eai-agent-gateway \
     eai-agent-gateway="$IMAGE" \
     -n "$NS"

   kubectl set image deployment/eai-agent-gateway-worker \
     eai-agent-gateway-worker="$IMAGE" \
     -n "$NS"
   ```

   Using the immutable commit tag is preferred over bare `latest` because the
   Deployment then records exactly which GitHub Actions artifact is running.

5. Wait for rollout completion.

   ```bash
   kubectl rollout status deployment/eai-agent-gateway -n "$NS" --timeout=180s
   kubectl rollout status deployment/eai-agent-gateway-worker -n "$NS" --timeout=180s
   ```

6. Confirm pod readiness.

   ```bash
   kubectl get pods -n "$NS" \
     -l 'app in (eai-agent-gateway,eai-agent-gateway-worker)' \
     -o wide
   ```

   Expected:

   - `eai-agent-gateway`: all replicas `Running`, `READY 2/2`
   - `eai-agent-gateway-worker`: all replicas `Running`, `READY 2/2`
   - no restart loop

7. Confirm the running image IDs.

   ```bash
   kubectl get pods -n "$NS" -o jsonpath='
   {range .items[?(@.metadata.labels.app=="eai-agent-gateway")]}
   {.metadata.name}{"\t"}{range .status.containerStatuses[*]}{.imageID}{" "}{end}{"\n"}{end}
   {range .items[?(@.metadata.labels.app=="eai-agent-gateway-worker")]}
   {.metadata.name}{"\t"}{range .status.containerStatuses[*]}{.imageID}{" "}{end}{"\n"}{end}'
   ```

   The Gateway container image ID should match the image digest reported by
   GHCR for `HEAD_SHA`. Ignore the Istio sidecar digest when comparing.

8. Verify internal health from inside the cluster.

   ```bash
   kubectl exec -n "$NS" deploy/eai-agent-gateway -- \
     wget -qO- http://127.0.0.1:8000/health

   kubectl exec -n "$NS" deploy/eai-agent-gateway-worker -- \
     wget -qO- http://127.0.0.1:8081/health
   ```

   Expected API health has `status: "healthy"` and healthy checks for
   `google_agent_engine`, `postgres`, `rabbitmq`, and `redis`.

   The external staging route can return `403 RBAC: access denied` depending on
   ingress policy. Treat internal health plus rollout status as the primary
   deployment validation unless an authenticated external smoke is available.

## Deploy Kubernetes manifest changes

When changes touch `k8s/staging/`, apply the manifests before setting or
restarting the image:

```bash
kubectl apply -k k8s/staging/
```

Then repeat the rollout and verification steps above. If `kubectl apply`
rewrites the image back to `:latest`, run `kubectl set image ... "$IMAGE"` again
so the deployed version remains explicit.

## Restart without changing image

Use a plain rollout restart only for config reloads or when the Deployment image
already points at the desired image digest:

```bash
kubectl rollout restart deployment/eai-agent-gateway -n "$NS"
kubectl rollout restart deployment/eai-agent-gateway-worker -n "$NS"

kubectl rollout status deployment/eai-agent-gateway -n "$NS" --timeout=180s
kubectl rollout status deployment/eai-agent-gateway-worker -n "$NS" --timeout=180s
```

Do not assume this deploys the latest build. If `.spec.template.spec.containers`
is pinned to an older digest, restarting the pod will keep running that older
digest.

## `REASONING_ENGINE_ID`

Do not update `REASONING_ENGINE_ID` for Gateway-only changes such as:

- request validation
- enum additions
- handlers
- logs or metrics
- rate limits
- Kubernetes probes or resource settings

Update `REASONING_ENGINE_ID` only when `app-eai-agent-engine` produced a new
staging Reasoning Engine and Gateway traffic should move to it.

Preferred path:

1. Update the `REASONING_ENGINE_ID` value in the staging Infisical secret source
   for `eai-agent-gateway`.
2. Let the Infisical operator sync the Kubernetes Secret.
3. Restart both Deployments if the auto-reload annotation does not restart them.

Emergency Kubernetes patch:

```bash
NEW_ID="<new-reasoning-engine-id>"

kubectl patch secret eai-agent-gateway-secrets -n "$NS" \
  -p "{\"data\":{\"REASONING_ENGINE_ID\":\"$(echo -n "$NEW_ID" | base64)\"}}"

kubectl rollout restart deployment/eai-agent-gateway -n "$NS"
kubectl rollout restart deployment/eai-agent-gateway-worker -n "$NS"
kubectl rollout status deployment/eai-agent-gateway -n "$NS" --timeout=180s
kubectl rollout status deployment/eai-agent-gateway-worker -n "$NS" --timeout=180s
```

The patch can be overwritten by the next Infisical sync. Use it only as an
operational bridge and then reconcile the source of truth.

## Rollback

Rollback a bad Gateway image by returning to the previous Git commit image:

```bash
PREVIOUS_SHA="<previous-good-commit-sha>"
IMAGE="$IMAGE_REPO:$PREVIOUS_SHA"

kubectl set image deployment/eai-agent-gateway \
  eai-agent-gateway="$IMAGE" \
  -n "$NS"

kubectl set image deployment/eai-agent-gateway-worker \
  eai-agent-gateway-worker="$IMAGE" \
  -n "$NS"

kubectl rollout status deployment/eai-agent-gateway -n "$NS" --timeout=180s
kubectl rollout status deployment/eai-agent-gateway-worker -n "$NS" --timeout=180s
```

`kubectl rollout undo` is acceptable only when the Deployment history clearly
points to the desired previous image. Prefer the explicit commit SHA when the
rollback target is known.

Rollback a bad Engine switch by restoring the previous `REASONING_ENGINE_ID` in
Infisical and restarting API plus worker.
