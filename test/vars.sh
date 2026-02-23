#!/bin/bash

## ===== General environment variables for the Percona Operator tests =====
export OPERATOR_ROOT_PATH=${OPERATOR_ROOT_PATH:-${PWD}}
echo "OPERATOR_ROOT_PATH=${OPERATOR_ROOT_PATH}"

## ======= Upstream DB operators params for testing ===============

# Recommended DB engine version available in PREVIOUS_PXC_OPERATOR_VERSION
export PSMDB_OPERATOR_VERSION=${PSMDB_OPERATOR_VERSION:-"1.21.1"}
echo "PSMDB_OPERATOR_VERSION=${PSMDB_OPERATOR_VERSION}"

export PSMDB_DB_ENGINE_VERSION=${PSMDB_DB_ENGINE_VERSION:-"8.0.12-4"}
echo "PSMDB_DB_ENGINE_VERSION=${PSMDB_DB_ENGINE_VERSION}"

# Recommended DB engine version available in PREVIOUS_PSMDB_OPERATOR_VERSION
export PREVIOUS_PSMDB_DB_ENGINE_VERSION=${PREVIOUS_PSMDB_DB_ENGINE_VERSION:-"7.0.15-9"}
echo "PREVIOUS_PSMDB_DB_ENGINE_VERSION=${PREVIOUS_PSMDB_DB_ENGINE_VERSION}"

# Previous versions of the operators for testing upstream DB operators upgrades.
export PREVIOUS_PSMDB_OPERATOR_VERSION=${PREVIOUS_PSMDB_OPERATOR_VERSION:-"1.19.1"}
echo "PREVIOUS_PSMDB_OPERATOR_VERSION=${PREVIOUS_PSMDB_OPERATOR_VERSION}"

## ============== K3D cluster configuration ===================
# export KUBECONFIG="${KUBECONFIG:-${OPERATOR_ROOT_PATH}/test/kubeconfig}"
# echo "KUBECONFIG=${KUBECONFIG}"

