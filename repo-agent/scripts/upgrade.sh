#!/bin/bash

# Copyright 2026 The Gemini For Kubernetes Development Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

# This script automates the upgrade process for repo-agent.
# It follows these steps:
# - Cleanup old resources
# - Upgrade the installation
# - Scale down controllers
# - Run post-upgrade mutations (if any)
# - Scale up controllers

REPO_AGENT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_AGENT_ROOT}"

cleanup() {
  echo "--- Cleanup ---"
  # Rarely cleanups are required.
  #./scripts/ops/scaledown_and_clean.py --types=sandboxes --apply
  #./scripts/ops/scaledown_and_clean.py --types=sandboxtasks --apply
}

upgrade() {
  echo "--- Upgrade ---"
  # This applies new manifests and restarts the components.
  # We use the latest images by default, or a specific tag if provided.
  make update-repo-agent-latest IMAGE_TAG="${IMAGE_TAG:-latest}" SKIP_PREQS="${SKIP_PREQS:-false}"
}

scaledown() {
  echo "--- Scale down ---"
  # Step 2 might have scaled it back up during rollout.
  # We scale it down again to ensure it's safe for mutations.
  kubectl scale sts -n repo-agent-system repowatch-controller --replicas=0
}

mutations() {
  echo "--- Mutations ---"
  # The upgrade script is expected to be modified when we have new migrations to be done.

  # 3/1
  #./scripts/ops/mutate_repowatches.py --mutator set-kcc-workspace-disk-size-20Gi-030126 --apply
  echo "No mutations to run."
}

scaleup() {
  echo "--- Scale up ---"
  kubectl scale sts -n repo-agent-system repowatch-controller --replicas=1
}

# Run the upgrade process
cleanup ## required for https://github.com/gke-labs/gemini-for-kubernetes-development/pull/803 which removed devc- prefix from service,Sandbox,pods
upgrade
scaledown
mutations
scaleup

echo "Upgrade completed successfully!"
