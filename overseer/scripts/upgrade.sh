#!/bin/bash
# Copyright 2026 The Kubernetes Authors.
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

# This script automates the upgrade process for overseer.
# It follows these steps:
# - Upgrade the installation

OVERSEER_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${OVERSEER_ROOT}"

upgrade() {
  echo "--- Upgrade ---"
  # This applies new manifests and restarts the components.
  # We use the latest images by default, or a specific tag if provided.
  make update-latest IMAGE_TAG="${IMAGE_TAG:-latest}" SKIP_PREQS="${SKIP_PREQS:-false}"
}

# Run the upgrade process
upgrade

echo "Upgrade completed successfully!"
