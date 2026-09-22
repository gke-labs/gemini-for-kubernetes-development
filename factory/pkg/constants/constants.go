package constants

const (
	KeyGithubToken     = "GITHUB_TOKEN"
	KeyGeminiAPIKey    = "GEMINI_API_KEY"
	KeyAnthropicAPIKey = "ANTHROPIC_API_KEY"
	KeyGithubLogin     = "GITHUB_LOGIN"
	KeyGithubEmail     = "GITHUB_EMAIL"
	SecretFactoryUser  = "factory-user"
	// BYO-project deploy defaults (Workload Identity carries the
	// credentials; these only say where to deploy).
	KeyGcpProject = "GCP_PROJECT"
	KeyGcpRegion  = "GCP_REGION"
)
