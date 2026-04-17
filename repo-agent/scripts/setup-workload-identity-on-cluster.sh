#!/bin/bash
# Copyright 2026.
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

set -x
export PROJECT_ID=$(gcloud config get-value project)
if [[ -z "$PROJECT_ID" ]]; then
    echo "Error: PROJECT_ID is not set. Please set your default project using 'gcloud config set project <id>'"
    exit 1
fi
export REGION=${REGION:-us-central1}

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

