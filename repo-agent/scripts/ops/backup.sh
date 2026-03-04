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

# Backup Sandboxes
echo "Backing up Sandboxes..."
kubectl get sandboxes -A -o yaml > "${BACKUP_DIR}/sandboxes.yaml" || echo "Warning: Failed to backup Sandboxes"

echo "Backup complete: ${BACKUP_DIR}"
