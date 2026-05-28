# OpenEverest Percona Server MongoDB Provider

This repository contains the official provider for deploying and managing [Percona Server for MongoDB](https://www.percona.com/mongodb/) (PSMDB) on Kubernetes through [OpenEverest](https://openeverest.io/).

The Percona Server for MongoDB provider enables OpenEverest to orchestrate the lifecycle of MongoDB deployments, including provisioning, scaling, backup and restore operations, and monitoring integration.

For more information about OpenEverest, visit the [main OpenEverest repository](https://github.com/openeverest/openeverest).

## Quick Start

To deploy Percona Server for MongoDB through OpenEverest:

```bash
# Add the provider Helm repository
helm repo add provider-percona-server-mongodb https://openeverest.github.io/provider-percona-server-mongodb/
helm repo update

# Deploy the provider Helm chart
helm install provider-percona-server-mongodb provider-percona-server-mongodb/provider-percona-server-mongodb \
  --namespace everest-system

```

## Features

The PSMDB provider supports:

- **Topologies**: Replica Set, Sharded Cluster
- **Components**: MongoDB Engine, Mongos (for sharded clusters), Backup agents
- **Backup & Restore**: Snapshots and point-in-time recovery using Percona Backup for MongoDB (PBM)
- **Monitoring**: Integration with Percona Monitoring and Management (PMM)
- **Lifecycle Management**: Create, scale, update, and delete MongoDB clusters
- **High Availability**: Automatic failover and recovery for replica sets

## Configuration

The provider supports configuration through OpenEverest Instance CRDs. Common configuration options include:

- **Topology Type**: Choose between `replicaSet` or `sharded` deployments
- **Component Sizing**: Configure CPU, memory, and storage for each component
- **Version**: Specify the MongoDB version to deploy
- **Storage**: Configure persistent volume claims for data and backup storage
- **Backup Configuration**: Enable and configure automated backups

## Architecture

The provider implements the OpenEverest ProviderInterface, which handles:

- **Sync**: Reconciles Instance specifications with MongoDB operator-managed resources
- **Status**: Reports cluster health and readiness
- **Cleanup**: Removes MongoDB clusters and associated resources
- **Watches**: Monitors operator-native resources for changes

The provider bridges OpenEverest's declarative API with the [Percona Kubernetes Operator for MongoDB](https://www.percona.com/doc/kubernetes-operator-for-psmdb/index.html).

## Contributing

Contributions are welcome. If you find issues with this provider or have suggestions for improvements, please open an issue or submit a pull request in this repository.

For broader questions about OpenEverest or to contribute to the core project, see the [main repository](https://github.com/openeverest/openeverest).

## License

This provider is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for details.

Percona Server for MongoDB is licensed under the Server Side Public License (SSPL). For more information, visit the [Percona website](https://www.percona.com/software/mongodb/percona-server-for-mongodb).
