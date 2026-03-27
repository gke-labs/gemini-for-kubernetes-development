#!/bin/bash

set -x
# 1. Set up variables based on your current environment
export PROJECT_ID=$(gcloud config get-value project)
export K8S_SA="issue-sandbox"
export GCP_SA_NAME="kcc-overseer-sa"
export GCP_SA_EMAIL="${GCP_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"

if [[ -z "$K8S_NAMESPACE" ]]; then
    echo "Error: K8S_NAMESPACE is not set. Please set it"
    exit 1
fi

# 2. Create the GCP Service Account
gcloud iam service-accounts create $GCP_SA_NAME \
    --project=$PROJECT_ID \
    --display-name="KCC Overseer SA"

# 3. Grant the GCP Service Account broad permissions on the project
# (The e2e tests create/delete many types of resources)
gcloud projects add-iam-policy-binding $PROJECT_ID \
    --member="serviceAccount:${GCP_SA_EMAIL}" \
    --role="roles/owner"

# 4. Bind the Kubernetes Service Account to the GCP Service Account
gcloud iam service-accounts add-iam-policy-binding $GCP_SA_EMAIL \
    --project=$PROJECT_ID \
    --role="roles/iam.workloadIdentityUser" \
    --member="serviceAccount:${PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SA}]"

# 5. Annotate the Kubernetes Service Account
kubectl annotate serviceaccount $K8S_SA \
    --namespace $K8S_NAMESPACE \
    iam.gke.io/gcp-service-account=$GCP_SA_EMAIL
