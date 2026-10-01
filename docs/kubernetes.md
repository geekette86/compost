# Deploying Compost on Kubernetes

This guide puts the Compost proxy in a Kubernetes cluster, then points
Composer (on laptops or CI runners) at it.

The manifests are in [`deploy/kubernetes/`](../deploy/kubernetes):

| File | What it creates | Why |
|------|-----------------|-----|
| `namespace.yaml` | Namespace `compost` | Keeps everything in one place |
| `configmap.yaml` | ConfigMap `compost-config` | The proxy's `compost.json` (mirrors, TTLs) |
| `pvc.yaml` | PersistentVolumeClaim `compost-heap` (20Gi) | The heap: cached files survive restarts |
| `deployment.yaml` | Deployment `compost` (1 pod) | Runs the proxy |
| `service.yaml` | Service `compost` (port 80) | Stable address inside the cluster |
| `ingress.yaml` | Ingress (optional) | HTTPS address outside the cluster |
| `kustomization.yaml` | — | Ties it all together for `kubectl apply -k` |

## Before you start

You need:

* A cluster and `kubectl` connected to it (`kubectl get nodes` works).
* A default StorageClass for the volume (`kubectl get storageclass`).
* The image `ghcr.io/geekette86/compost:v0.1.0`. It is built by the
  [Release workflow](../.github/workflows/release.yml) when a `v*` tag is
  pushed. GitHub makes new packages **private**. Either make the package public
  (GitHub → your profile → Packages → compost → Package settings → Change
  visibility), or create an image pull secret in the cluster.

## Step 1: GitHub token (recommended)

Most PHP packages are downloaded from GitHub. Without a token GitHub allows
only 60 requests per hour, which a single `composer install` can use up.

Create a token at https://github.com/settings/tokens (a classic token with
**no scopes** is enough for public packages), then:

```bash
kubectl create namespace compost
kubectl -n compost create secret generic compost-github --from-literal=token=ghp_yourTokenHere
```

The secret is optional: without it Compost still works, just rate limited.

## Step 2: Deploy

From the repository root:

```bash
kubectl apply -k deploy/kubernetes
```

Wait until the pod is ready:

```bash
kubectl -n compost rollout status deployment/compost
kubectl -n compost get pods
```

## Step 3: Check it works

```bash
kubectl -n compost port-forward svc/compost 8080:80
```

In another terminal:

```bash
curl http://localhost:8080/healthz          # -> ok
curl http://localhost:8080/mirrors.json     # -> the list of mirrors
curl -sI http://localhost:8080/packagist/packages.json | grep X-Compost-Cache
# first time: MISS, second time: HIT
```

## Step 4: Point Composer at it

### CI runners inside the same cluster

Use the Service address. It is plain HTTP inside the cluster, so Composer must
allow non-HTTPS URLs:

```bash
composer global config allow-plugins.geekette86/compost true
composer global require geekette86/compost
composer config -g secure-http false
export COMPOST_URL=http://compost.compost.svc.cluster.local
composer install
```

For example, in a GitLab CI job running on Kubernetes:

```yaml
variables:
  COMPOST_URL: http://compost.compost.svc.cluster.local
before_script:
  - composer global config allow-plugins.geekette86/compost true
  - composer global require geekette86/compost
  - composer config -g secure-http false
```

### Laptops and machines outside the cluster

Expose Compost with HTTPS using the Ingress:

1. Edit `deploy/kubernetes/ingress.yaml`: set your host name, your ingress
   class and your cert-manager issuer.
2. Uncomment `- ingress.yaml` in `deploy/kubernetes/kustomization.yaml`.
3. `kubectl apply -k deploy/kubernetes`
4. On each machine:

   ```bash
   composer compost enable https://compost.example.com
   ```

> **Warning:** Compost has no login. Anyone who can reach the Ingress can
> download packages through it (but only the paths allowed in the config, so
> your GitHub token can't be misused for anything else). Restrict access with
> your ingress controller (IP allow list, VPN, or basic auth) if the cluster is
> public.

## Changing the configuration

Edit `deploy/kubernetes/configmap.yaml` (for example to add a mirror or
change a TTL), then:

```bash
kubectl apply -k deploy/kubernetes
kubectl -n compost rollout restart deployment/compost
```

The plugin re-reads `/mirrors.json` within an hour. Run `composer compost
status` to pick up new mirrors at once.

## Upgrading

Change `newTag` in `deploy/kubernetes/kustomization.yaml` to the new version
and run `kubectl apply -k deploy/kubernetes`. The cache stays on the volume.

## Good to know

* **Why only one replica?** The heap volume is `ReadWriteOnce`: only one pod
  can write to it. One small pod easily serves a whole team. The Deployment
  uses the `Recreate` strategy so the old pod lets go of the volume before the
  new one starts. Expect a few seconds of downtime during upgrades. The plugin
  falls back to downloading directly during that time.
* **Disk full?** Compost does not delete old files yet (see the roadmap). Grow
  the volume, or delete the PVC to start with an empty cache.
* **Stats:** `curl http://localhost:8080/stats` (through the port-forward)
  shows hits and misses per mirror.
* **Logs:** `kubectl -n compost logs deploy/compost -f`
* **Security:** the pod runs as a non-root user with a read-only root
  filesystem and no Linux capabilities.

## Removing Compost

```bash
kubectl delete -k deploy/kubernetes
```

This also deletes the cache volume and the namespace.
