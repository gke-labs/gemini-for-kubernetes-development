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

NAMESPACE="repo-agent-system"
DEPLOYMENT="syncer"
KSA_NAME="syncer"

usage() {
    echo "Usage:"
    echo "  For Kind/Standard Secret: $0 kind <path-to-service-account-key.json>"
    echo "  For GKE Workload Identity: $0 gke <gcp-project-id> <gcp-service-account-email>"
    exit 1
}

setup_kind() {
    KEY_FILE=$1
    if [ ! -f "$KEY_FILE" ]; then
        echo "Error: Key file '$KEY_FILE' not found."
        exit 1
    fi

    echo "Setting up credentials for Kind (using Secret)..."
    
    # Ensure namespace exists
    kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f - 

    # Create or update the secret
    kubectl create secret generic gcs-credentials \
        --from-file=key.json="$KEY_FILE" \
        -n $NAMESPACE \
        --dry-run=client -o yaml | kubectl apply -f - 

    echo "Secret 'gcs-credentials' created in namespace '$NAMESPACE'."
    echo "Restarting deployment to pick up the secret..."
    kubectl rollout restart deployment/$DEPLOYMENT -n $NAMESPACE
}

setup_gke() {
    PROJECT_ID=$1
    GSA_EMAIL=$2

    if [ -z "$PROJECT_ID" ] || [ -z "$GSA_EMAIL" ]; then
        usage
    fi

    echo "Setting up Workload Identity for GKE..."

    # 1. Bind the GCP Service Account to the Kubernetes Service Account
    echo "Binding GCP SA ($GSA_EMAIL) to KSA ($NAMESPACE/$KSA_NAME)..."
    gcloud iam service-accounts add-iam-policy-binding "$GSA_EMAIL" \
        --project="$PROJECT_ID" \
        --role="roles/iam.workloadIdentityUser" \
        --member="serviceAccount:$PROJECT_ID.svc.id.goog[$NAMESPACE/$KSA_NAME]"

    # 2. Annotate the Kubernetes Service Account
    echo "Annotating KSA..."
    kubectl annotate serviceaccount \
        --namespace $NAMESPACE \
        $KSA_NAME \
        iam.gke.io/gcp-service-account="$GSA_EMAIL" \
        --overwrite

    # 3. Patch the deployment to REMOVE the hardcoded GOOGLE_APPLICATION_CREDENTIALS env var
    #    so the client library falls back to Workload Identity.
    echo "Patching deployment to remove hardcoded GOOGLE_APPLICATION_CREDENTIALS env var..."
    kubectl patch deployment $DEPLOYMENT -n $NAMESPACE --type='json' -p='[{"op": "remove", "path": "/spec/template/spec/containers/0/env/0"}]'

    echo "Workload Identity setup complete. Deployment patched."
}

MODE=$1
shift

case "$MODE" in
    kind)
        setup_kind "$@"
        ;;
    gke)
        setup_gke "$@"
        ;;
    *)
        usage
        ;;
esac
