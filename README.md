# Percona Server MongoDB Provider

This directory contains a implementation of a Percona Server MongoDB (PSMDB) provider.

## 🚀 Quick Start

### Prerequisites

1. A Kubernetes cluster:
   ```
   make k3d-cluster-up
   ```

2. Generate Provider CR manifests (if changed):
   ```bash
   make gen
   ```

3. Install CRDs:
   ```bash
   make install
   ```

### Run the Provider

```bash
make run
```

### Create a Test Instance

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

```bash
make test-integration

# Or run directly:
. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl.yaml
```
