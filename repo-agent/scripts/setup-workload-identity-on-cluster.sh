#!/bin/bash

set -x
export PROJECT_ID=$(gcloud config get-value project)
export REGION=us-central1

if [[ -z "$CLUSTER_NAME" ]]; then
    echo "Error: CLUSTER_NAME is not set. Please set it"
    exit 1
fi

# 1. Enable Workload Identity on the GKE cluster
gcloud container clusters update $CLUSTER_NAME \
--region=$REGION \
--project=$PROJECT_ID \
--workload-pool=${PROJECT_ID}.svc.id.goog

# 2. Enable Workload Identity on the node pool
gcloud container node-pools update default-pool \
--cluster=$CLUSTER_NAME \
--region=$REGION \
--project=$PROJECT_ID \
--workload-metadata=GKE_METADATA

