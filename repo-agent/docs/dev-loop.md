# Dev loop: iterate against the live cluster without merging

A merge → image build → rollout cycle is about ten minutes. Most
changes do not need it: `pkg/clients` falls back to your kubeconfig
when it is not running in a pod, and the controllers use
`ctrl.GetConfigOrDie()`, which does the same. So the API and the
controller run on your laptop against the real cluster, and the only
things that stay remote are sandboxes — which is where you want them.

## The loops

| What you changed | Loop | Cycle |
|---|---|---|
| API handler, controller logic | `make dev-api` / `make dev-controller` | seconds |
| Task prompts, factory scripts | `make dev-controller` (builds factory locally) | seconds |
| React | `make dev-ui` | hot reload |
| Anything, in-cluster | `dev/tools/push-images` + `kubectl set image` | 1–3 min |

### API

```sh
make dev-api DEV_USER=<your member namespace>
```

`REPO_AGENT_DEV_USER` attributes every request to that user instead of
a gateway session — the API logs a loud warning at startup, and the
variable is defined nowhere in the manifests, so it cannot leak into a
deployment.

### Controller (and prompts)

```sh
make dev-controller
```

This scales the in-cluster controller to zero first, because **exactly
one controller may run against a cluster** — two reconcilers means
duplicate runs. It builds `factory` into `bin/` and puts it on PATH,
so prompt changes (embedded via `go:embed`) take effect on the next
start: edit `runbook_execute.txt`, restart, click Run.

It restores the in-cluster replica on exit. If the process is killed
hard:

```sh
make dev-restore     # nothing reconciles until you do
```

### UI

```sh
make dev-ui          # proxies /api to localhost:8080
```

## Rules

1. **One controller at a time**, and put it back when you finish. A
   cluster with no controller looks idle and quietly stops reconciling
   claims, watches, and pauses.
2. **Local runs are real runs.** They create real sandboxes, real
   clusters, real spend. The loop is fake; the consequences are not.
3. **Workload Identity does not follow you home.** Code paths that use
   the controller's WI — Secret Manager resolution — fail locally with
   no metadata server, and fall back to pasted secrets.
4. **Never set `REPO_AGENT_DEV_USER` in a manifest.** It is a laptop
   affordance; in a cluster it is an authentication bypass.
