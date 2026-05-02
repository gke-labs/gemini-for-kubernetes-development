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

set -e

# Default backup directory
BACKUP_ROOT="./backups"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
BACKUP_DIR="${BACKUP_ROOT}/${TIMESTAMP}"

mkdir -p "${BACKUP_DIR}"

echo "Backing up resources to ${BACKUP_DIR}..."

# Backup Repowatch
echo "Backing up RepoWatches..."
kubectl get repowatches -A -o yaml > "${BACKUP_DIR}/repowatches.yaml" || echo "Warning: Failed to backup RepoWatches"

# Backup Sandbox
echo "Backing up Sandboxes..."
kubectl get sandboxes -A -o yaml > "${BACKUP_DIR}/sandboxes.yaml" || echo "Warning: Failed to backup Sandboxes"

echo "Backup complete: ${BACKUP_DIR}"
