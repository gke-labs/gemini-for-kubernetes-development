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
export PROJECT_ID="${PROJECT_ID:-$(gcloud config get-value project)}"
export REGION="${REGION:-us-central1}"
export CLUSTER_NAME="${CLUSTER_NAME:-gemini-dev}"

# 1. Enable Workload Identity on the cluster
gcloud container clusters update ${CLUSTER_NAME} \
    --region=${REGION} \
    --workload-pool=${PROJECT_ID}.svc.id.goog

# 2. Enable Workload Identity on node pools
# Note: You may need to do this for each node pool
gcloud container node-pools update default-pool \
    --cluster=${CLUSTER_NAME} \
    --region=${REGION} \
    --workload-metadata=GKE_METADATA
