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
# 1. Set up variables based on your current environment
export PROJECT_ID=$(gcloud config get-value project)
export GSA_NAME="repo-agent-sa"
export WORKLOAD_IDENTITY_POOL="${PROJECT_ID}.svc.id.goog"
export NAMESPACE="repo-agent"
export KSA_NAME="repo-agent"

# 2. Create the GCP Service Account (GSA)
gcloud iam service-accounts create ${GSA_NAME} \
    --display-name "Repo Agent GCP Service Account"

# 3. Assign necessary roles to the GSA
# For example, to allow it to read from GCR/AR
gcloud projects add-iam-policy-binding ${PROJECT_ID} \
    --member "serviceAccount:${GSA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com" \
    --role "roles/artifactregistry.reader"

# 4. Allow the Kubernetes Service Account (KSA) to impersonate the GSA
gcloud iam service-accounts add-iam-policy-binding ${GSA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com \
    --role "roles/iam.workloadIdentityUser" \
    --member "serviceAccount:${PROJECT_ID}.svc.id.goog[${NAMESPACE}/${KSA_NAME}]"
