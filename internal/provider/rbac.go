// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

// Run `make manifests` to regenerate config/rbac/role.yaml from these markers.
// This file contains kubebuilder RBAC markers for controller-gen.
// See: https://book.kubebuilder.io/reference/markers/rbac

// Base RBAC (required by all providers):
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.openeverest.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// =============================================================================
// PSMDB-SPECIFIC RBAC — Percona Server MongoDB Operator integration.
// =============================================================================
// +kubebuilder:rbac:groups=psmdb.percona.com,resources=perconaservermongodbs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=psmdb.percona.com,resources=perconaservermongodbs/status,verbs=get
// +kubebuilder:rbac:groups=psmdb.percona.com,resources=perconaservermongodbs/finalizers,verbs=update
// +kubebuilder:rbac:groups=psmdb.percona.com,resources=perconaservermongodbbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=psmdb.percona.com,resources=perconaservermongodbbackups/status,verbs=get
// +kubebuilder:rbac:groups=psmdb.percona.com,resources=perconaservermongodbrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=psmdb.percona.com,resources=perconaservermongodbrestores/status,verbs=get

// =============================================================================
// OPENEVEREST BACKUP RESOURCES
// =============================================================================
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backupclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backupstorages,verbs=get;list;watch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores/status,verbs=get;update;patch

// =============================================================================
// MONITORING RESOURCES
// =============================================================================
// +kubebuilder:rbac:groups=monitoring.openeverest.io,resources=monitoringconfigs,verbs=get;list;watch

// =============================================================================
// CORE KUBERNETES RESOURCES
// =============================================================================
// +kubebuilder:rbac:groups="",resources=secrets;configmaps;services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
