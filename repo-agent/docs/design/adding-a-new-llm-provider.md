# Adding a New LLM Provider

This document provides a step-by-step guide for developers to extend the Gemini Code Repo Agent by integrating a new Large Language Model (LLM) provider.

The system uses a standard `Provider` interface, which ensures that new providers can be added smoothly. This guide will walk you through implementing this interface, registering your new provider, and making it available for use in `RepoWatch` resources.

## 1. The `Provider` Interface

The core of the provider system is the `Provider` interface, located in `pkg/llm/provider.go`. Any new LLM provider must implement this interface.

```go
// pkg/llm/provider.go

type Provider interface {
	Setup(workspacesDir, tokensDir string) error
	Cleanup(workspacesDir string) error
	Run(prompt string) ([]byte, error)
	AddPostProcessor(p PostProcessor)
}
```

### Method Explanations

-   `Setup(workspacesDir, tokensDir string) error`: This method is called before running the LLM. It is responsible for any necessary setup, such as reading API keys from the `tokensDir` and setting them as environment variables, or preparing configuration files in the `workspacesDir`.
-   `Cleanup(workspacesDir string) error`: This method is called after the LLM run is complete. It should be used to clean up any temporary files or configurations created during the `Setup` phase.
-   `Run(prompt string) ([]byte, error)`: This is the primary method that executes the LLM. It takes the prompt as input and should return the raw output from the LLM as a byte slice.
-   `AddPostProcessor(p PostProcessor)`: This method adds a function to a slice of post-processors that are run sequentially on the raw LLM output. For example, you can add the included `StripYAMLMarkers` post-processor to automatically clean up code blocks.

## 2. Step-by-Step Implementation Guide

Here is how to create a new provider called `MyProvider`.

### Step 2.1: Create the Provider File

Create a new file for your provider in the `pkg/llm/` directory. For this example, we will call it `pkg/llm/my_provider.go`.

### Step 2.2: Define the Provider Struct and Implement the Interface

In your new file, define a struct for your provider and implement the methods from the `Provider` interface. You can use the existing `gemini.go` implementation as a template.

```go
// pkg/llm/my_provider.go

package llm

import (
	"fmt"
	// Add any other necessary imports for your provider's SDK, etc.
)

// Ensure MyProvider implements the Provider interface.
var _ Provider = &MyProvider{}

type MyProvider struct {
	// Add any fields your provider needs, like an API client.
	processors []PostProcessor
}

func (m *MyProvider) AddPostProcessor(p PostProcessor) {
	m.processors = append(m.processors, p)
}

func (m *MyProvider) Setup(workspacesDir, tokensDir string) error {
	// TODO: Implement your setup logic.
	// For example, read an API key from a file in tokensDir and initialize a client.
	// apiKey, err := os.ReadFile(filepath.Join(tokensDir, "my-provider-key"))
	// if err != nil {
	// 	return fmt.Errorf("failed to read API key: %w", err)
	// }
	// m.client = myapi.NewClient(string(apiKey))
	fmt.Println("MyProvider setup complete.")
	return nil
}

func (m *MyProvider) Cleanup(workspacesDir string) error {
	// TODO: Implement your cleanup logic if needed.
	fmt.Println("MyProvider cleanup complete.")
	return nil
}

func (m *MyProvider) Run(prompt string) ([]byte, error) {
	// TODO: Implement the logic to call your LLM's API.
	// rawOutput, err := m.client.Generate(prompt)
	// if err != nil {
	// 	return nil, err
	// }

	// This is a placeholder implementation.
	rawOutput := []byte(fmt.Sprintf("Response from MyProvider for prompt: %s", prompt))

	// Apply any post-processors.
	var err error
	for _, p := range m.processors {
		rawOutput, err = p(rawOutput)
		if err != nil {
			return nil, err
		}
	}

	return rawOutput, nil
}
```

## 3. Registering the New Provider

To make the system aware of your new provider, you must add it to the `NewLLMProvider` factory function located in `pkg/llm/provider.go`.

Modify the `switch` statement to include a new case for your provider. This allows users to select it in their `RepoWatch` manifests.

**File:** `pkg/llm/provider.go`

```diff
 // ... existing code ...
 func NewLLMProvider(name string, outputStartIndicator string) (Provider, error) {
 	switch name {
 	case "gemini-cli":
 		g := &Gemini{Executor: &RealCommandExecutor{}}
 		g.AddPostProcessor(StripYAMLMarkers)
 		if outputStartIndicator != "" {
			g.AddPostProcessor(StripUnillStartIndicator(outputStartIndicator))
		}
 		return g, nil
 	case "claude":
 		c := &Claude{}
 		c.AddPostProcessor(StripYAMLMarkers)
 		return c, nil
+	case "my-provider": // Add your provider's name here
+		 m := &MyProvider{}
+		 m.AddPostProcessor(StripYAMLMarkers) // Optionally add default post-processors
+		 return m, nil
 	default:
 		return nil, fmt.Errorf("unknown provider: %s", name)
 	}
 }
 // ... existing code ...
```

## 4. Updating the API Types

To ensure your new provider is a valid option in the `RepoWatch` Custom Resource Definition (CRD), you need to add its name to the `LLMConfig` type definition.

**File:** `repowatch/api/v1alpha1/repowatch_types.go`

1.  **Add a new constant for your provider's name.**

    ```diff
     const (
     	// GeminiProvider represents the Gemini LLM provider.
     	GeminiProvider = "gemini-cli"
     	// ClaudeProvider represents the Claude LLM provider.
     	ClaudeProvider = "claude"
    +	// MyProvider represents our new custom provider.
    +	MyProviderName = "my-provider"
     )
    ```

2.  **Add the name to the `+kubebuilder:validation:Enum` list.** This enforces that only registered provider names can be used in the manifest.

    ```diff
     // Provider is the name of the LLM provider to use. This field is used to
     // determine which LLM client to instantiate and how to interact with the
     // LLM API.
    -// +kubebuilder:validation:Enum=gemini-cli;claude
    +// +kubebuilder:validation:Enum=gemini-cli;claude;my-provider
     // +kubebuilder:default=gemini-cli
     Provider string `json:"provider,omitempty"`
    ```


## 5. Creating the API Key Secret (Local Development)

For local development and testing, API key secrets are typically created in the `repo-agent-system` namespace using the `create-secrets` target in the `Makefile`. This provides a convenient way to provision secrets from environment variables.

### Step 5.1: Update the `Makefile` `create-secrets` Target

Add a `kubectl create secret` command to the `create-secrets` target in your `Makefile`. This command will create a Kubernetes Secret containing your provider's API key, sourced from an environment variable.

**File:** `Makefile`

```diff
 .PHONY: create-secrets
 create-secrets:
	kubectl create namespace repo-agent-system || true
	@kubectl create secret -n repo-agent-system generic gemini-vscode-tokens --from-literal=gemini=${GEMINI_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic github-pat --from-literal=pat="${GITHUB_PAT}" --from-literal=name="`git config --global user.name`" --from-literal=email=`git config --global user.email` --dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret -n repo-agent-system generic anthropic-api-key --from-literal=claude=${ANTHROPIC_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
+	@ifndef MY_PROVIDER_API_KEY
+	$(warning MY_PROVIDER_API_KEY is not set. MyProvider will not work.)
+	@else
+	@kubectl create secret -n repo-agent-system generic my-provider-secret --from-literal=mykey=${MY_PROVIDER_API_KEY} --dry-run=client -o yaml | kubectl apply -f -
+	@endif
	# Create github-token secret for the API, optionally including OAuth credentials
```

### Step 5.2 (Optional): Add `ifndef` Environment Variable Check

It's good practice to add an `ifndef` check at the top of your `Makefile` to provide a warning or error if the environment variable for your new provider's API key is not set. This helps ensure users are aware of missing prerequisites.

**File:** `Makefile` (top section)

```diff
 # Check pre-reqs
+ifndef MY_PROVIDER_API_KEY
+$(warning MY_PROVIDER_API_KEY is not set. MyProvider will not work.)
+endif

 ifndef GEMINI_API_KEY
 $(error GEMINI_API_KEY is not set. Please set it before running make.)
 ```

## 6. Making the API Key Available to the Agent

For the new provider's API key to be accessible by the agent sandboxes, its Kubernetes Secret must be copied from the `repo-agent-system` namespace into the user's personal namespace when they first log in. This bootstrapping process is handled by the `review-api` service.

### Step 6.1: Register the Secret in the API Service

**File:** `review-ui/review-api/main.go`

First, define a new constant for your provider's secret name. This secret must be created in the `repo-agent-system` namespace.

```diff
 const (
	// ... existing constants
	githubSecretName = "github-pat"
	geminiSecretName = "gemini-vscode-tokens"
+	myProviderSecretName = "my-provider-secret" // The name of the secret holding your provider's key
 )
```

### Step 6.2: Add Secret-Copying Logic

Next, add logic to the `bootstrapNamespace` function to copy this secret into new user namespaces.

**File:** `review-ui/review-api/main.go`

```diff
 func bootstrapNamespace(ctx context.Context, targetNS string) error {
	// ... existing namespace creation and other secret copies ...
	if err := copySecret(ctx, systemNamespace, geminiSecretName, targetNS, geminiSecretName); err != nil {
		log.Printf("Warning: failed to copy default gemini secret: %v", err)
	}
+	if err := copySecret(ctx, systemNamespace, myProviderSecretName, targetNS, myProviderSecretName); err != nil {
+		log.Printf("Warning: failed to copy my-provider secret: %v", err)
+	}

	if err := setupServiceAccounts(ctx, targetNS); err != nil {
		log.Printf("Warning: failed to setup service accounts: %v", err)
	}

	return nil
 }
```

### Step 6.3: How the Secret is Mounted and Used

This new logic ensures that the secret is available in the user's namespace. The end-to-end flow for the key is as follows:

1.  A user references the secret name in their `RepoWatch` manifest (e.g., `apiKeySecretRef: my-provider-secret`).
2.  The `repowatch-controller` reads this reference and configures the agent sandbox Pod to mount the specified Secret as a volume.
3.  The volume is mounted to a known directory inside the agent pod, such as `/etc/llm-tokens/`.
4.  The `Provider.Setup()` method, which you implemented in Step 2, receives this path in its `tokensDir` argument.
5.  Your `Setup` logic can now read the key from the file system (e.g., `os.ReadFile(filepath.Join(tokensDir, "your-key-name-in-secret"))`) and use it to configure your LLM client.

## 7. Usage

Once the steps above are complete, users can select your new provider in any `RepoWatch` manifest by setting the `provider` field in the `llm` configuration.

```yaml
# ... inside a RepoWatch manifest ...
spec:
  review:
    llm:
      provider: my-provider # Use the new provider
      apiKeySecretRef: my-provider-api-key-secret
      prompt: "Please review this pull request."
# ... rest of the manifest ...
```
