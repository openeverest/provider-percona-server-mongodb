# Percona Server MongoDB Provider

This directory contains a implementation of a Percona Server MongoDB (PSMDB) provider.

## Installation

The provider chart is published as an OCI artifact to the GitHub Container
Registry. It bundles the Percona Operator for MongoDB as a subchart, so a single
install brings up both the provider and the operator.

```bash
helm install provider-percona-server-mongodb \
  oci://ghcr.io/openeverest/charts/provider-percona-server-mongodb \
  --version 0.1.0 \
  --create-namespace
```

Upgrade to a newer chart version:

```bash
helm upgrade provider-percona-server-mongodb \
  oci://ghcr.io/openeverest/charts/provider-percona-server-mongodb \
  --version 0.1.0
```

Uninstall:

```bash
helm uninstall provider-percona-server-mongodb
```

> Browse available versions on the
> [chart package page](https://github.com/openeverest/provider-percona-server-mongodb/pkgs/container/charts%2Fprovider-percona-server-mongodb).

## 🚀 Quick Start

### Prerequisites

1. A Kubernetes cluster:

   ```
   make k3d-cluster-up
   ```

2. Generate Provider CR manifests (if changed):

   ```bash
   make generate
   ```

3. Install CRDs:
   ```bash
   make install
   ```

### Run the Provider

```bash
make run
```

### Create a Test

```bash
kubectl apply -f examples/instance-simple.yaml
```

Watch the provider logs and check the PSMDB resource:

```bash
kubectl get psmdb
kubectl get instance
```

## 🧪 Running Integration Tests

The `test/integration/` directory contains kuttl tests that verify the provider's behavior.

### Prerequisites for Tests

1. SDK CRDs installed (see Quick Start above)
2. Provider running in the background:
   ```bash
   make run
   ```

### Running the Tests

```bash
# From the examples directory:
make test-integration

# Or run directly:
cd examples
. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl.yaml
```

**Note:** The tests assume the provider is already running and will create/update/delete Instance resources to verify correct behavior.
