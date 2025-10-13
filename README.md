# Gemini for Kubernetes Development

The gemini-for-kubernetes-development is a Gemini CLI extension to automate core development tasks within the kubernetes/kubernetes repository.

## Installation

Install the gemini-for-kubernetes-development extension by running the following command from your terminal *(requires Gemini CLI v0.6.0 or newer)*:

```bash
gemini extensions install https://github.com/gke-labs/gemini-for-kubernetes-development
```

If you do not yet have Gemini CLI installed, or if the installed version is older than 0.6.0, see
[Gemni CLI installation instructions](https://github.com/google-gemini/gemini-cli?tab=readme-ov-file#-installation).

## Use the extension

The extension adds following command to Gemini CLI
- `/dv:enable {<group>/<kind>}`: enable Declarative Validation for a given k8s API resource

## Resources

- [Gemini CLI extensions](https://github.com/google-gemini/gemini-cli/blob/main/docs/extension.md): Documentation about using extensions in Gemini CLI
