# Provider development with Tilt

This directory contains a [Tilt](https://tilt.dev/) setup for developing
`provider-percona-server-mongodb`. It installs the latest released OpenEverest
v2 core and then builds and deploys this provider, with live-reload on every
code change.

You do **not** need a local checkout of the OpenEverest core.

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Helm](https://helm.sh/docs/intro/install/)
- [k3d](https://k3d.io/)
  - **Linux:** load the `br_netfilter` kernel module before creating the
    cluster (`sudo modprobe br_netfilter`). Without it, Service ClusterIPs
    blackhole for pods sharing a node with their backend and replica sets never
    become ready — see [Troubleshooting](#troubleshooting).
- [Tilt](https://docs.tilt.dev/install.html)

## Quick start

```bash
# 1. (Optional) configure the environment
cp dev/.env.example dev/.env

# 2. Create the local cluster and start Tilt
make dev-up
```

Tilt opens its dashboard at <http://localhost:10350>. Once everything is green:

- The Everest UI/API is available at <http://localhost:8080>
  (default credentials: `admin` / `admin`).
- Apply an example Instance to exercise the provider:

  ```bash
  kubectl apply -f examples/instance-example.yaml
  kubectl get instances -w
  ```

Edit any provider Go code and Tilt rebuilds the binary and live-updates the
running pod without recreating it.

To tear things down:

```bash
make dev-down      # stop Tilt (keeps the cluster)
make dev-destroy   # stop Tilt and delete the cluster
```

## Using a cluster you already have

`make dev-up` creates a local k3d cluster, which is what we recommend for day-to-day
work. Development itself only needs *a* cluster, though - kind, GKE, or a shared dev
cluster work just as well, and are often faster for multi-node testing. Skip
`make dev-up`, point Tilt at the context, and push images to a registry the cluster
can pull from:

```bash
cp dev/.env.example dev/.env   # set K8S_CONTEXT and DOCKER_REGISTRY_URL
tilt up -f dev/Tiltfile
```

## Configuration

All settings live in `dev/.env` (see `dev/.env.example`). Common options:

| Variable | Default | Description |
|----------|---------|-------------|
| `INSTALL_OPENEVEREST` | `true` | Install the released OpenEverest core. |
| `OPENEVEREST_VERSION` | _(latest)_ | Pin a specific core chart version. |
| `PROVIDER_NAMESPACE` | `default` | Namespace for the provider + DB operator. |
| `ENABLE_MINIO` | `false` | Deploy MinIO + a `BackupStorage` CR for backups. |
| `K8S_CONTEXT` | _(unset)_ | Restrict Tilt to a specific Kubernetes context. |
| `DOCKER_REGISTRY_URL` | _(unset)_ | Push images here instead of the cluster's local registry. |

> **Note:** While OpenEverest v2 is in pre-release, the Helm repository only
> publishes pre-release tags (e.g. `2.0.0-dev.1`). Helm's "latest" resolution
> skips pre-releases, so you must set `OPENEVEREST_VERSION` explicitly until
> v2.0.0 is generally available.

## Developing the core and the provider together

When you need to test against a locally built core (not a release), run two
Tilt instances against the same cluster:

1. In the **openeverest** core repo, start the core dev environment
   (`make dev-up`). It manages `everest-system` and the core CRDs.
2. In this repo, start the provider Tilt instance on a different port with the
   core installation disabled:

   ```bash
   INSTALL_OPENEVEREST=false tilt up -f dev/Tiltfile --port 10351
   ```

The two instances manage disjoint Kubernetes objects, so they run side by side
without conflicting. With `INSTALL_OPENEVEREST=false`, the OpenEverest core
CRDs are expected to already exist in the cluster (installed by the core Tilt
instance).

## Troubleshooting

### A replica set never becomes ready (DNS times out on some pods)

The Instance hangs in `Provisioning`, `rs.status()` reports members as
`(not reachable/healthy)`, and the affected pods restart in a loop on liveness
probe timeouts — but which pods are affected changes on every `make dev-up`.
On Linux this means `br_netfilter` is not loaded on the host, so kube-proxy's
DNAT is bypassed for traffic that stays on one node's bridge. Pods sharing a
node with CoreDNS lose DNS entirely, so the primary cannot resolve its peers
and never propagates the replset config. Confirm with:

```bash
docker logs k3d-provider-psmdb-test-server-0 2>&1 | grep br_netfilter
```

Then load the module and recreate the cluster:

```bash
sudo modprobe br_netfilter
echo br_netfilter | sudo tee /etc/modules-load.d/k8s.conf  # persist
make dev-destroy && make dev-up
```
