# Warm pool for research sandboxes

Status: proposed — feasibility established, not built
Owner: barney-s
Date: 2026-09-28
Scope: factory (primary), repo-agent (call sites + the filler), UI (untouched)

## Motivation

Starting a research conversation takes minutes before the member can ask
anything. The board files a claim, the controller runs `factory research
start`, and only after a sandbox exists, its pod is running, envd is
answering and the repo is cloned can acpd take a prompt. The UI papers
over it with a `requested` row and a `starting` state, and
`researchTimeout` allows twenty minutes for the worst case.

This note asks whether keeping one or two pre-warmed sandboxes ready
would remove that wait, what it would cost, and what would have to
change. It is written to be picked up later; the build is deferred.

## What the wait actually is (measured, not assumed)

From the events of a real cold start — `rsch-substrate-fcc0dd70`,
namespace `barney-s`, 2026-09-28 17:12Z:

```
FailedScheduling        0/9 nodes are available: ... 9 Insufficient memory   (x3, ~1 min)
Scheduled               -> gk3-repo-agent-1-pool-1-0a2b5cae-c6h2
SuccessfulAttachVolume  pvc-e668f71d-...
Pulling                 ghcr.io/gke-labs/.../factory-golang:latest
Pulled                  in 1m21.411s.  Image size: 1990394794 bytes
Started
```

| Phase | Cost | Warmable? |
| --- | --- | --- |
| Scheduling / node scale-up | ~1 min here, unbounded when the cluster is full | Yes — a pooled pod is already scheduled |
| PVC provision + attach | Not separately visible; same minute as `Scheduled` | Yes, but there is nothing to win |
| Image pull | **1m21s for 2.0 GB** | Yes — a pooled pod already has the image |
| envd ready + `git clone` | Not in pod events | Clone is repo-specific; see below |

Two things follow. The disk is **not** a cost worth designing around —
an earlier draft of this analysis ranked it alongside scheduling and the
pull, and the data does not support that. And the cluster was full:
`0/9 nodes are available … Insufficient memory`, with nine research pods
live in one namespace, the oldest two days old, each requesting
500m CPU / 2Gi memory / 6Gi ephemeral. The backlog of abandoned
conversations is itself a major cause of slow starts.

## Can the PVC be attached after the pod is created?

No, and it is worth writing down why so it is not re-asked.

`pod.spec.volumes` is immutable. The mutable set on a running pod is
essentially the container image, added tolerations,
`activeDeadlineSeconds`, and in-place resource resize behind a feature
gate. Kubernetes has no volume hotplug. Here it is stricter still: the
disk comes from `volumeClaimTemplates` on the Sandbox spec
(`pkg/sandbox/manifests.go:236`), StatefulSet-style, mounted at
`/workspaces`.

Nor can a pool be made of PVCs alone. The cluster default StorageClass
is `standard-rwo` with `volumeBindingMode: WaitForFirstConsumer`, so an
unbound PVC provisions nothing — the disk is created when a pod is
scheduled onto a node. On the storage axis there is no middle ground
between a whole warm pod and nothing at all.

None of which blocks the pool: a pooled pod is created with its claim
and by claim time the disk is provisioned, attached and mounted. The
claim never touches storage.

## Why one pool can serve every repoboard

**The research pod spec contains nothing repo-specific.** Walking
`EnsureResearchSandbox` (`pkg/sandbox/research.go:146`) field by field —
image, env (`HOME`, `GOCACHE`, `GOMODCACHE`, `TMPDIR`),
secrets (member-level, not repo-level), resources,
`DeployerServiceAccount`, ports, the 10Gi claim at `/workspaces` — every
one is a global flag or a constant.

Repo-specificity is entirely:

1. **Metadata** — the name `rsch-<repo slug>-<short id>`, and the
   `repo` / `cloneURL` / `htmlURL` annotations.
2. **An action after readiness** — `cloneForResearch`
   (`pkg/commands/research.go:313`) cloning into `/workspaces/<repo>`
   over envd.

So a claim mutates labels and annotations only: no pod spec change, no
restart, no re-attach. **One repo-agnostic pool serves every board.**

A per-repo pool — the shape assumed when this was first sketched — is
the wrong axis. It would multiply pods by boards to pre-pay only the
clone, while the repo-agnostic pool pre-pays scheduling and the image
pull, which is where the time is.

Note the unit: sandboxes are namespaced per member, so "two warm pods"
means two *per member namespace*, not two globally.

## Two things that already work in our favour

`researchViewFromSandbox`
(`repo-agent/pkg/api/handlers_research.go:181`) skips any research
sandbox with no `research-session-id` annotation — "a research sandbox
with no session id cannot be addressed". An unclaimed pool pod has no
session id by definition, so it is invisible to `/api/research` for
free. No new filter, no ghost rows.

`findResearchSandbox` (`handlers_research.go:366`) already resolves
session to sandbox **by label**, then confirms identity from the
annotation because the label holds only a 32-bit digest prefix. The
API's read path needs no change.

## The name is the blocker, and it is a shallow one

`ResearchSandboxName(repo, sessionID)` is
`rsch-<repo slug>-<sha256(sessionID)[:8]>`, derived in
`pkg/sandbox/research.go:90` and mirrored in
`repo-agent/pkg/factorycli/research.go:51`. Deriving it is what makes
creation idempotent today: a retried start recomputes the same name and
finds the sandbox it already made instead of running a second engine on
one conversation.

A pool pod exists before its session does, so it cannot carry that
digest, and `metadata.name` is immutable. The pool therefore mints a
random short id at fill time and the session adopts the pod — the
inversion already predicted in the comment at `research.go:78`.

Every place that derives the name has to resolve it instead:

| Site | Today | After |
| --- | --- | --- |
| `pkg/sandbox/research.go:150` | get-by-name, create on miss | find-by-label, claim, create |
| `pkg/commands/research.go:171` | save-notes derives the name | resolve it — **load-bearing**, see below |
| `repo-agent/pkg/controllers/repoboard/research.go:94` | claim key `member/<name>` | `member/<sessionID>` |
| `repoboard/research.go:112`, `:195`, `requests.go:385` | "does the sandbox exist yet" by name | by session annotation |
| `repo-agent/pkg/api/handlers_research.go:348`, `handlers_board_research.go:160` | report the name the sandbox *will* have | drop it |

The save-notes one is load-bearing rather than cosmetic:
`envd.Connect` dials `<sandboxName>-lb.<ns>.svc.cluster.local`
(`pkg/envd/client.go:147`), so a claimed pod is reachable only under its
pool-era Service name. A derived name would 404 and every save would
fail.

Dropping the predicted name from the two API responses is safe: the UI
never reads it. `Research.js:1715` takes only `sessionId` and `title`
from the start-session response, and the terminal link uses
`info.sandbox` off the live session view.

## The claim

```go
// ClaimPoolSandbox adopts a warm sandbox for one session, or reports
// that the pool was empty.
func ClaimPoolSandbox(ctx, kube, ns, repo, sessionID, cloneURL, htmlURL string) (string, error) {
	free, err := list(ns, LabelResearchPool+"=free")
	if err != nil {
		return "", err
	}
	for i := range free.Items {
		sb := &free.Items[i]
		if terminating(sb) {
			continue
		}
		labels := sb.GetLabels()
		labels[LabelResearchSession] = ResearchShortID(sessionID)
		delete(labels, LabelResearchPool)
		sb.SetLabels(labels)
		sb.SetAnnotations(merge(sb.GetAnnotations(),
			researchAnnotations(repo, sessionID, cloneURL, htmlURL)))

		// The object carries the resourceVersion it was read at, so a
		// second claimer racing for this pod gets a 409 and moves on.
		// That conflict IS the mutual exclusion: no lease, no leader
		// election, no second source of truth about who owns a pod.
		_, err := update(ns, sb)
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		return sb.GetName(), nil
	}
	return "", errPoolEmpty
}
```

`EnsureResearchSandbox` becomes three steps: **find by label** (replacing
today's get-by-name, preserving idempotence) → **claim** → **create a
dedicated sandbox** when the pool is empty. That last fallback is what
makes the pool safe to ship: it is a fast path and never a dependency,
so a filler that is broken, throttled or not yet deployed degrades to
exactly today's behaviour.

After the claim, the flow is unchanged — connect over envd, clone, print
the `RESEARCH_SANDBOX_READY` line.

## Changes

**factory** — new `pkg/sandbox/pool.go`:

- `LabelResearchPool = "sandbox.gemini.google.com/research-pool"`, value
  `free`
- `PoolSandboxName()` → `rsch-pool-<8 random hex>`
- `EnsurePool(ctx, kube, ns, size, ...)` — count free, create the
  difference
- `ClaimPoolSandbox(...)` as above
- `FindResearchSandbox(ctx, kube, ns, sessionID)` — list by session
  label, verify by annotation

Changed: `EnsureResearchSandbox`, `runResearchSaveNotes`, and a new
`factory research pool --size N` verb.

**repo-agent** — the five call sites in the table above, plus the
filler. The cheapest filler is the repoboard controller invoking
`factory research pool --size N` per member namespace on its periodic
pass, reusing `factorycli.Runner`'s single-flight exactly as every other
verb does. No new controller, no new RBAC.

`FindResearchSandbox` is worth having on its own merits, pool or no
pool: it removes the last places where two modules must agree on a
string derivation rather than reading the object.

## Risks

**A stale pool serves stale binaries.** The image is `:latest`. A pod
warmed on Monday is still running Monday's factory when claimed on
Thursday — and acpd *is* `factory daemon` in that pod, so this is a
correctness hazard, not cosmetic staleness. The same effect has already
been observed once: the `waiting` field from #1638 stayed false on
pre-existing sandboxes because their pods ran the old binary. Pool
entries need a max age (~12h) so the pool rotates onto new images.

**Double-claim is a new failure mode.** Today two concurrent starts for
one session compute the same name and the loser gets `AlreadyExists`.
With a pool they can claim two different pods and run two engines on one
conversation. The controller's per-session single-flight is the primary
guard; a post-claim re-check that releases the duplicate back to the
pool is the backstop.

**The pool competes for the capacity it is trying to save.** Two idle
pods per member at 500m / 2Gi / 6Gi, on Autopilot, where the last
research pod was rejected by all nine nodes three times before
scheduling. Shipping the pool alone makes every other workload's cold
start worse.

**The clone stays on the critical path** and becomes the dominant
remaining cost, sized by the repository.

## Alternatives that should be tried first

**Mirror the image to Artifact Registry and enable Image Streaming.**
GKE Image Streaming starts a container before the image finishes
pulling, and it works only for Artifact Registry — our image is on
ghcr.io, so we get none of it today. This targets the measured 1m21s
directly, holds no warm capacity, and requires no code change at all.
Shrinking the 2 GB image is the other half of the same lever.

**Reap idle research sandboxes.** Nine multi-day-old pods in one
namespace are what made every node ineligible. A TTL on untouched
conversations returns the capacity that makes cold starts slow in the
first place, and it fixes the tail — the unbounded scale-up case the
twenty-minute timeout exists for — which is the part members actually
notice. It is far simpler than a pool and it is a prerequisite for one.

## Recommendation

Do the Artifact Registry move and the reaper first, then re-measure a
cold start. If it lands near thirty seconds, the claim-by-label refactor
is not worth its complexity and this note can be closed unbuilt. If
scheduling still dominates, the pool is the answer and the design above
is ready to implement.
