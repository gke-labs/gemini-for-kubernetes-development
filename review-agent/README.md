

## Pre-requisites
* Get Gemini API key from https://aistudio.google.com
* Get Github developer PAT (classic token) from: https://github.com/settings/tokens
* Kind https://kind.sigs.k8s.io/
* kubectl https://kubernetes.io/docs/tasks/tools/
* helm https://helm.sh/docs/intro/install/

## Quick start

```bash
export GEMINI_API_KEY="..."
export GITHUB_PAT="..."

make               ## builds, creates kind cluster, deploys everything there
kubectl apply -f examples/repowatch.yaml
make port-forward  ## localhost:13380/ 
```

UI can be accessed from localhost:13380/ or <hostname>:13380/

Wait for sometime for the PR reviews to show up in the UI.