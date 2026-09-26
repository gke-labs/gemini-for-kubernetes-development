# GCP access for deployments: Workload Identity, bring-your-own project

Runbook deployments (the Runs tab's deploy/upgrade scenarios) sometimes
need real GCP resources — a GKE cluster, a
VM, cloud APIs. This document explains how sandbox agents get that
access, what is stored where, and how to grant and revoke it.

**The short version: nothing secret is stored, anywhere.** You grant a
Google-managed identity access to *your* project; revoking is deleting
one IAM binding on your side.

## How it works

Runbook and research sandboxes run as a Kubernetes ServiceAccount named
`factory-deployer` in your member namespace (created automatically, carrying **no** Kubernetes
RBAC). On GKE, [direct Workload Identity federation](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity)
makes that ServiceAccount a GCP principal:

```
principal://iam.googleapis.com/projects/<CLUSTER_PROJECT_NUMBER>/locations/global/workloadIdentityPools/<CLUSTER_PROJECT_ID>.svc.id.goog/subject/ns/<YOUR_NAMESPACE>/sa/factory-deployer
```

When an agent in your sandbox runs `gcloud` or any Google client
library, the GKE metadata server mints a **short-lived** token for that
principal. There is no key file, nothing to leak from a secret, and
nothing to rotate.

The Settings page shows your namespace's exact principal and a
ready-to-run grant command.

## Setup (two steps, both yours)

1. **Settings → GCP Deployment**: enter your project ID and default
   region. These are stored (they are not secrets — they only say
   *where* deployments land) and exported to deploy tasks as
   `GOOGLE_CLOUD_PROJECT` / `CLOUDSDK_CORE_PROJECT` /
   `CLOUDSDK_COMPUTE_REGION`.
2. **Grant the principal access in your project** (copy-paste from
   Settings):

   ```sh
   gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
     --member "principal://…/subject/ns/YOUR_NAMESPACE/sa/factory-deployer" \
     --role roles/editor --condition None
   ```

   `roles/editor` is the simple default; grant narrower roles if you
   know what the runbook needs (`roles/container.admin` for
   GKE-only work, for example). The runbook's **What this needs**
   section states what each scenario assumes.

## Revoking

```sh
gcloud projects remove-iam-policy-binding YOUR_PROJECT_ID \
  --member "principal://…/subject/ns/YOUR_NAMESPACE/sa/factory-deployer" \
  --role roles/editor
```

Effective within minutes; running agents lose access when their current
short-lived token expires.

## Security properties and boundaries

- **No stored credentials.** The platform never holds a key, token, or
  password for your project. The grant lives in *your* project's IAM
  policy, visible and auditable there.
- **Per-member identity.** The principal names your namespace: agents
  in other members' namespaces are different principals with no access
  to your project unless you grant them.
- **Scope of exposure.** Any agent task running in *your* runbook or
  research sandbox can use the identity while the binding exists. Treat the
  grant as "my repo-agent may touch this project" — use a dedicated
  project, not a production one.
- **Only runbook and research sandboxes** run as `factory-deployer`. Fix,
  review, plan and triage sandboxes keep the default pod identity and
  have no GCP reach.
- **Cleanup is a runbook contract.** Every runbook's Teardown section
  is the cleanup path; resources a deploy creates should be labeled and
  torn down when you're done. Budget alerts on your project are a good
  belt-and-suspenders.

## Alternatives considered

- **Service-account key JSON pasted into Settings** — rejected as the
  default: long-lived exfiltratable credential, and many organizations
  disable SA key creation outright. May appear later as a discouraged
  fallback for non-GKE installations.
- **Auto-created (vended) projects** — a managed-platform feature
  (billing, folders, budgets, liability). The Settings field doesn't
  care who filled it, so a vending flow can slot in later without
  redesign.
- **A single shared platform project** — quota fights, cross-member
  interference, ambiguous cleanup. Not offered.
- **`gcloud auth login` in the web terminal** — technically possible,
  but parks a human's broad credentials on a PVC that agents use. Don't.

## Importing secrets from GCP Secret Manager

The GitHub PAT and engine API keys can be **referenced** instead of
pasted: enter a Secret Manager resource name
(`projects/YOUR_PROJECT/secrets/NAME/versions/latest`) in Settings
under the corresponding field. The pasted mechanism is unchanged and
still works; a reference wins when both are set.

**How it works.** The board controller — the only component that ever
talks to Secret Manager — resolves references with its own Workload
Identity and materializes the values exactly where pasted values land
today (the `factory-user` working secret). Nothing else changes
downstream.

**Setup.** Grant the controller's sync principal (shown in Settings)
access to each secret you reference:

```sh
gcloud secrets add-iam-policy-binding SECRET_NAME --project YOUR_PROJECT \
  --member "principal://…/ns/repo-agent-system/sa/repowatch-controller" \
  --role roles/secretmanager.secretAccessor
```

**What this buys.**

- **No pasting**: the raw secret never transits your browser or
  clipboard, and only a resource name is stored here.
- **Rotation is a new version**: point at `versions/latest`, add a
  version in your project, the sync picks it up within ~10 minutes.
- **Revocation is one IAM binding**, on your side, audited in your
  project's logs.

**Boundaries.** The controller caches resolved values for ~10 minutes.
The working copies still materialize as Kubernetes Secrets in your
namespace (that is what tasks consume); reference mode removes the
paste-and-forget copy as the *source of truth*, not the working set.
