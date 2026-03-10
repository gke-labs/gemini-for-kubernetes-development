# Gemini for Kubernetes Development

A Gemini CLI extension to automate core development tasks within the `kubernetes/kubernetes` repository, including declarative validation authoring, PR review, and SIG API Machinery issue triage.

## Installation

Install the extension by running the following command from your terminal *(requires Gemini CLI v0.6.0 or newer)*:

```bash
gemini extensions install https://github.com/gke-labs/gemini-for-kubernetes-development
```

If you do not yet have Gemini CLI installed, or if the installed version is older than 0.6.0, see
[Gemini CLI installation instructions](https://github.com/google-gemini/gemini-cli?tab=readme-ov-file#-installation).

## Use the extension

The extension adds the following skills to Gemini CLI:

- **declarative-validation-authoring** — Enable Declarative Validation for a Kubernetes API resource. Walks through adding `+k8s:*` validation tags to versioned types, updating strategy files, marking handwritten validation for migration, writing tests, and running code generation.

- **declarative-validation-review** — Review a pull request that adds or modifies Declarative Validation. Produces a structured review report covering tag correctness, handwritten validation migration, test coverage analysis, and common pitfalls.

- **apimachinery-issue-triage** — Triage issues in the `kubernetes/kubernetes` repository for SIG API Machinery. Evaluates issues, applies labels, routes to domain experts, and manages issue lifecycle.

## Resources

- [Gemini CLI extensions](https://github.com/google-gemini/gemini-cli/blob/main/docs/extension.md): Documentation about using extensions in Gemini CLI
