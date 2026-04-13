#!/bin/bash

# This script sets up Workload Identity for a Kubernetes Service Account to impersonate a GCP Service Account.
# example:
# K8S_SA=overseer-sandbox K8S_NAMESPACE=overseer-system ./repo-agent/scripts/setup-gcp-access-workload-identity.sh

set -x
# 1. Set up variables based on your current environment
export PROJECT_ID=$(gcloud config get-value project)
export K8S_SA="${K8S_SA:-issue-sandbox}"
export GCP_SA_NAME="kcc-overseer-sa"
export GCP_SA_EMAIL="${GCP_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"

if [[ -z "$K8S_NAMESPACE" ]]; then
    echo "Error: K8S_NAMESPACE is not set. Please set it"
    exit 1
fi

# 2. Create the GCP Service Account
if ! gcloud iam service-accounts describe "$GCP_SA_EMAIL" --project="$PROJECT_ID" >/dev/null 2>&1; then
    gcloud iam service-accounts create $GCP_SA_NAME \
        --project=$PROJECT_ID \
        --display-name="KCC Overseer SA"
else
    echo "GCP Service Account ${GCP_SA_EMAIL} already exists."
fi

# 3. Grant the GCP Service Account broad permissions on the project
# (The e2e tests create/delete many types of resources)
if ! gcloud projects get-iam-policy "$PROJECT_ID" \
    --flatten="bindings[].members" \
    --format="table(bindings.role)" \
    --filter="bindings.members:serviceAccount:${GCP_SA_EMAIL}" | grep -q "roles/owner"; then
    gcloud projects add-iam-policy-binding $PROJECT_ID \
        --member="serviceAccount:${GCP_SA_EMAIL}" \
        --role="roles/owner"
else
    echo "GCP Service Account ${GCP_SA_EMAIL} already has roles/owner on project ${PROJECT_ID}."
fi

# 4. Bind the Kubernetes Service Account to the GCP Service Account
K8S_MEMBER="serviceAccount:${PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SA}]"
if ! gcloud iam service-accounts get-iam-policy "$GCP_SA_EMAIL" \
    --project="$PROJECT_ID" \
    --flatten="bindings[].members" \
    --format="table(bindings.role)" \
    --filter="bindings.members:${K8S_MEMBER}" | grep -q "roles/iam.workloadIdentityUser"; then
    gcloud iam service-accounts add-iam-policy-binding $GCP_SA_EMAIL \
        --project=$PROJECT_ID \
        --role="roles/iam.workloadIdentityUser" \
        --member="${K8S_MEMBER}"
else
    echo "Workload Identity binding already exists."
fi

# 5. Annotate the Kubernetes Service Account
kubectl annotate serviceaccount $K8S_SA \
    --namespace $K8S_NAMESPACE \
    iam.gke.io/gcp-service-account=$GCP_SA_EMAIL \
    --overwrite
