
## Kind Cluster

To get a GCP Service Account JSON key file, you can use the gcloud CLI.

Here is the sequence of commands to create a new Service Account, give it permission to write to GCS, and download the key file.

1. Set your variables


```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export SA_NAME="repo-agent-syncer"
```

2. Create the Service Account

```bash
gcloud iam service-accounts create $SA_NAME \
  --display-name "Repo Agent Syncer" \
  --project ${PROJECT_ID}
```

3. Grant Permissions (Storage Object Admin)
This gives the service account permission to read/write to GCS buckets.

```bash
gcloud projects add-iam-policy-binding ${PROJECT_ID} \
  --member "serviceAccount:${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --role "roles/storage.objectAdmin" --condition=None
```

4. Create and Download the Key File
This command generates the JSON key and saves it to key.json in your current directory.

```
gcloud iam service-accounts keys create bin/key.json \
  --iam-account "${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com" \
  --project ${PROJECT_ID}
```

5. Setup the syncer credentials

```bash
./setup-syncer-creds.sh kind bin/key.json
```

## GKE with Workload Identity

Instructions for setting up the syncer pod with Workload Identity on a GKE cluster.

1. Enable Workload Identity on your GKE Cluster (if not already enabled)

You can enable Workload Identity for an existing cluster using:

```bash
export PROJECT_ID="YOUR_GCP_PROJECT_ID"
export REGION="your region"
export CLUSTER_NAME="your cluster name"

gcloud container clusters update ${CLUSTER_NAME} \
  --region=${REGION} \
  --workload-pool=${PROJECT_ID}.svc.id.goog
```

2. Create a GCP Service Account (GSA)

This Service Account will have the necessary permissions to interact with Google Cloud Storage.

```bash
export PROJECT_ID="YOUR_GCP_PROJECT_ID"
export GSA_NAME="repo-agent-syncer"
export GSA_EMAIL="${GSA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
 
gcloud iam service-accounts create "${GSA_NAME}" \
  --display-name "Repo Agent Syncer" \
  --project "${PROJECT_ID}"
```

3. Grant GCS Permissions to the GSA

Grant the GSA the Storage Object Admin role (or a more restrictive role if preferred) to allow it to read and write to GCS buckets.

```bash
gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
  --member "serviceAccount:${GSA_EMAIL}" \
  --role "roles/storage.objectAdmin"
```

4. Kubernetes Service Account (KSA) Setup and Binding (using the provided script)

The syncer-deployment.yaml already defines a Kubernetes Service Account named syncer in the repo-agent-system namespace. We need to annotate this KSA and bind it to the GSA.

You can use the setup-syncer-creds.sh script to perform these steps, which also patches the deployment to remove the conflicting `GOOGLE_APPLICATION_CREDENTIALS` environment variable.

```bash
./setup-syncer-creds.sh gke "${PROJECT_ID}" "${GSA_EMAIL}"
```

 After running this script, your syncer pod should be able to authenticate with GCS using Workload Identity.

