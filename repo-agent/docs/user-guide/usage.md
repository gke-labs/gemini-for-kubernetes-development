# Usage

Repo Agent is a work queue for repositories. Once installed, the UI opens on
the **Work** page: a single table per board answering "what do I do next" —
open issues and pull requests you are involved in, what stage each item is
in, and what needs your attention.

## Boards

A board is a `RepoBoard` custom resource scoped to one repository. Create one
from the UI by pasting a repository URL on the Work page, or apply the
example manifest:

```bash
kubectl apply -f examples/repoboard.yaml
```

Work is never declared in the board spec — it is discovered live from GitHub
(issues and PRs you are involved in) and from running factory sandboxes.

## Fixes and reviews

- **Fix**: claims the issue by assigning it to you on GitHub (the audit
  trail lives on the GitHub side) and launches a `factory fix` sandbox under
  your identity. The resulting PR, comments and commits are authored by you,
  not a bot.
- **Review**: requests a self-review on the PR and launches `factory pr
  review`. The draft review stays in the UI until you publish it — nothing
  is posted to GitHub without your click.
- **Publish / Promote / Merge**: human-gated writes, always executed with
  your own GitHub token.

Every GitHub write is authored by the human who initiated it. No board
setting can authorize execution under someone else's identity: work only
runs for a user when that user consented (they clicked, self-assigned, or
opted in to auto-fix themselves).

See [the RepoBoard design](../design/repoboard.md) for the full model:
shared boards, trigger labels, intake (triage and draft reviews), and the
two-key auto-fix consent.
