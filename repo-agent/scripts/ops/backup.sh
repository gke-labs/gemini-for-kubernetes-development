#!/bin/bash
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

# Backup ReviewSandbox
echo "Backing up ReviewSandboxes..."
kubectl get reviewsandboxes -A -o yaml > "${BACKUP_DIR}/reviewsandboxes.yaml" || echo "Warning: Failed to backup ReviewSandboxes"

# Backup IssueSandbox
echo "Backing up IssueSandboxes..."
kubectl get issuesandboxes -A -o yaml > "${BACKUP_DIR}/issuesandboxes.yaml" || echo "Warning: Failed to backup IssueSandboxes"

echo "Backup complete: ${BACKUP_DIR}"
