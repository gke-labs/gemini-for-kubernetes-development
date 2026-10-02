# Gemini CLI extension for Kubernetes development

A Gemini CLI extension to automate core development tasks within the `kubernetes/kubernetes` repository, including declarative validation authoring, PR review, and SIG API Machinery issue triage.

## Installation

The extension lives in `extras/gemini-extension/` of this repository, so install it from a clone *(requires Gemini CLI v0.6.0 or newer)*:

```bash
git clone https://github.com/gke-labs/gemini-for-kubernetes-development.git
gemini extensions install ./gemini-for-kubernetes-development/extras/gemini-extension
```

Use `gemini extensions link <path>` instead if you are editing the extension and want changes picked up without reinstalling.

> Earlier versions were installed straight from the repository URL, when the extension sat at the repository root. That no longer works: uninstall it (`gemini extensions uninstall gemini-for-kubernetes-development`) and install from the path above.

If you do not yet have Gemini CLI installed, or if the installed version is older than 0.6.0, see
[Gemini CLI installation instructions](https://github.com/google-gemini/gemini-cli?tab=readme-ov-file#-installation).

## Use the extension

The extension adds the following skills to Gemini CLI:

- **declarative-validation-authoring** — Enable Declarative Validation for a Kubernetes API resource. Walks through adding `+k8s:*` validation tags to versioned types, updating strategy files, marking handwritten validation for migration, writing tests, and running code generation.

- **declarative-validation-review** — Review a pull request that adds or modifies Declarative Validation. Produces a structured review report covering tag correctness, handwritten validation migration, test coverage analysis, and common pitfalls.

- **apimachinery-pr-triage** — Triage pull requests in `kubernetes/kubernetes` for SIG API Machinery.

- **apimachinery-issue-triage** — Triage issues in the `kubernetes/kubernetes` repository for SIG API Machinery. Evaluates issues, applies labels, routes to domain experts, and manages issue lifecycle.

It also adds two commands: `/dv:enable` (enable declarative validation for an API or subresource) and `/dv:review <PR>` (review a declarative validation PR).

## Resources

- [Gemini CLI extensions](https://github.com/google-gemini/gemini-cli/blob/main/docs/extension.md): Documentation about using extensions in Gemini CLI
