# Hitting GitHub less

Status: proposed.

The board spends its members' GitHub quota in three places: the RepoBoard
controller's reconcile, the API's board feed, and the `factory` processes
the controller launches. This note cuts what they spend, and makes the
board poll fast only while someone is looking at it.

## Today

**Conditional requests are already in place, for most of it.**
`pkg/ghquota` puts an HTTP cache (gregjones/httpcache) under every REST
client the controller and the API build: a repeated GET goes out with
`If-None-Match`, and GitHub's 304 costs no quota. Within GitHub's
`max-age=60` it does not go out at all. ghquota also holds a token's
calls until its rate limit resets.

What it misses, and what costs the most:

| | spend | why |
|---|---|---|
| `factory pr watch` | ~150 requests/h per watched PR, the member's | polls every 2m (the PR, check runs, statuses, commits, comments, paginated) through factory's own GitHub client, which has no cache; every fix PR gets a watch (`policy.autoIterate`) |
| the ETag cache on a shared repository | a full 200 instead of a 304 | httpcache keys an entry by URL alone and checks `Vary: Authorization` only on reading: two members' boards on one repository overwrite each other's entries |
| the controller's reconcile | ~60 reconciles/h per board | requeued every minute whatever happens; mostly 304s since #1958 bounded discovery's listings, but a round trip each, plus `ListReviews` again every reconcile for a PR a Review rule skips |
| the board feed | ~9 GraphQL points per rebuild, a rebuild per 60-80s while viewed | GraphQL has no ETags; cached 60s, and only rebuilt while the UI polls |
| the Try tab | every 10s while the tab is open | the runbook panel polls without the hidden/idle checks the feed has |

The controller and the API are separate deployments, and nothing tells
the controller a board is being looked at. Sandboxes are not watched: a
finished run is settled at the next requeue, so the minute requeue also
paces settlement.

## Proposal

### 1. One cached GitHub transport, factory included

factory's GitHub client (`factory/pkg/github`) gets the same transport the
board's has: an ETag cache under the token, and the rate-limit gate.
factory cannot import repo-agent, so the transport moves to factory
(`factory/pkg/github/transport`) and repo-agent's ghquota uses it from
there, as it already uses factory's other packages through the CLI only
— or keeps its own copy if a shared import is unwelcome; the behaviour is
what matters.

- `pr watch` is one process per PR for 55 minutes: its ticks are mostly
  unchanged resources, so its spend falls by most of its 150 requests an
  hour.
- The overseer's watch loops get the cache too. Their behaviour is
  unchanged; only what a repeated GET costs.

### 2. The cache keyed per token

One cache per token (by its hash), so two members' boards on one
repository keep their own ETags. Bounded: an LRU per token, so a
long-running controller's memory does not grow with every URL it ever
read. A restart still starts cold; a shared backend (redis) is left out
until it shows up.

### 3. Fast while viewed, slow otherwise

- **The API stamps a board when its feed is read.** `GET
  /api/board/:board/work` merge-patches
  `board.gemini.google.com/viewed-at` when the stamp is older than two
  minutes. The UI polls the feed only while its tab is visible, active
  and not idle, so the stamp means someone is looking. The stamp's
  patch wakes the reconciler at once: opening a board refreshes it.
- **The controller requeues by it:** every minute while `viewed-at` is
  within five minutes, every ten minutes otherwise.
- **Settlement does not wait for the slow requeue.** The controller
  watches its sandboxes (mapped to their board by
  `board.gemini.google.com/board`, or by repository), so a finished run
  is settled as it finishes; the requeue paces only GitHub discovery.
  Clicks and applies are unaffected: Requests are watched, and an
  apply requeues at 5s.

An unwatched board's automation picks new items up within ten minutes
instead of one.

### 4. Smaller

- **A Review rule's skip is remembered:** a PR whose owner already
  reviewed it is not asked again (`ListReviews`) until the PR changes
  (its `updated_at`).
- **The owner's login is cached** per token for fifteen minutes, instead
  of `GET /user` every reconcile.
- **The Try tab** polls like the feed: not while hidden or idle, and
  every two minutes, as the Runs state does.
- **The posting fast-poll** (a 3s feed rebuild while a write stands)
  rebuilds every 10s.

## Not now

- **Webhooks** need a GitHub App (boards use members' tokens, and repo
  webhooks need admin) and public ingress. A larger change.
- **The events API** as a change detector (304s are free): its latency
  is 30s to hours and pending reviews are not in it, so it could only
  thin full listings, not replace them.
- **The feed's GraphQL** stays: one query instead of four REST listings.

## Steps

1. factory's GitHub client gets the cached, rate-limit-aware transport
   (`pr watch` and the overseer with it).
2. ghquota's cache keyed per token, with an LRU.
3. `viewed-at` from the feed, the controller's adaptive requeue, and
   watching sandboxes for settlement.
4. The smaller ones: remembered Review skips, the cached login, the Try
   tab's polling, the posting poll's interval.
