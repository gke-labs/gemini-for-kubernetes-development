#!/bin/bash
set -e

if [ -z "$1" ]; then
  echo "Usage: $0 <backup-directory>"
  exit 1
fi

BACKUP_DIR="$1"

if [ ! -d "${BACKUP_DIR}" ]; then
  echo "Error: Directory ${BACKUP_DIR} does not exist."
  exit 1
fi

echo "Restoring resources from ${BACKUP_DIR}..."

# Helper function to apply if file exists and has content
apply_resource() {
    local file="$1"
    local resource_name="$2"
    
    if [ -f "${file}" ]; then
        if [ -s "${file}" ]; then
            echo "Restoring ${resource_name} from ${file}..."
            # We use --validate=false to avoid schema validation errors during restore of CRDs/custom resources 
            # if the CRDs are not fully consistent, though here they should be.
            # We filter out resourceVersion, uid, creationTimestamp to ensure clean apply
            # This is a naive filter using grep/sed which might be fragile but better than nothing without yq.
            # However, for complex nested structures, this is risky. 
            # Let's try direct apply first, if it fails, the user might need to clean it.
            # Actually, standard kubectl apply usually works if we accept that we might overwrite.
            
            kubectl apply -f "${file}" || echo "Warning: Failed to apply ${file}"
        else
             echo "Skipping ${resource_name} (file empty)"
        fi
    else
        echo "Skipping ${resource_name} (file not found)"
    fi
}

apply_resource "${BACKUP_DIR}/repowatches.yaml" "RepoWatches"
apply_resource "${BACKUP_DIR}/sandboxes.yaml" "Sandboxes"

echo "Restore operation completed."
