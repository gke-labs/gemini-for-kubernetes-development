package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/k8s"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewUserCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage factory users",
	}

	cmd.AddCommand(NewUserOnboardCommand(ctx))

	return cmd
}

type UserOnboardFlags struct {
	GithubLogin string
	GithubToken string
	GithubEmail string
	GeminiKey   string
}

func NewUserOnboardCommand(ctx context.Context) *cobra.Command {
	var flags UserOnboardFlags

	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Onboard a new user by creating a namespace and factory-user secret",
		RunE: func(_ *cobra.Command, _ []string) error {
			return RunUserOnboard(ctx, flags.GithubLogin, flags.GithubToken, flags.GithubEmail, flags.GeminiKey, false)
		},
	}

	cmd.Flags().StringVar(&flags.GithubLogin, "github-login", "", "GitHub username (if not provided, deduced from gh api user)")
	cmd.Flags().StringVar(&flags.GithubToken, "github-token", "", "GitHub Personal Access Token (if not provided, extracted from env or gh auth status)")
	cmd.Flags().StringVar(&flags.GithubEmail, "github-email", "", "GitHub email for commits (if not provided, deduced from gh api user or git config)")
	cmd.Flags().StringVar(&flags.GeminiKey, "gemini-key", "", "Gemini API Key (if not provided, extracted from GEMINI_API_KEY env)")

	return cmd
}

func RunUserOnboard(ctx context.Context, githubLogin, githubToken, githubEmail, geminiKey string, confirm bool) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh cli not installed")
	}

	cmdAuth := exec.CommandContext(ctx, "gh", "auth", "status")
	if err := cmdAuth.Run(); err != nil {
		return fmt.Errorf("gh cli not logged in (run 'gh auth login')")
	}

	if githubLogin == "" {
		out, err := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login").Output()
		if err != nil {
			return fmt.Errorf("failed to deduce github login using gh api: %w", err)
		}
		githubLogin = strings.TrimSpace(string(out))
		if githubLogin == "" {
			return fmt.Errorf("github login deduced from gh api is empty")
		}
	}

	if githubToken == "" {
		if val := os.Getenv("GITHUB_TOKEN"); val != "" {
			githubToken = val
		} else if val := os.Getenv("GH_TOKEN"); val != "" {
			githubToken = val
		} else {
			out, _ := exec.CommandContext(ctx, "gh", "auth", "status", "-t").CombinedOutput()
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				if strings.Contains(strings.ToLower(line), "token:") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						token := strings.TrimSpace(parts[1])
						if token != "" {
							githubToken = token
							break
						}
					}
				}
			}
			if githubToken == "" {
				homeDir, err := os.UserHomeDir()
				if err == nil {
					content, err := os.ReadFile(filepath.Join(homeDir, ".config", "gh", "hosts.yml"))
					if err == nil {
						lines := strings.Split(string(content), "\n")
						for _, line := range lines {
							if strings.Contains(line, "oauth_token:") {
								parts := strings.SplitN(line, ":", 2)
								if len(parts) == 2 {
									token := strings.TrimSpace(parts[1])
									if token != "" {
										githubToken = token
										break
									}
								}
							}
						}
					}
				}
			}
		}
	}

	if githubToken == "" {
		return fmt.Errorf("github token not provided and could not be extracted from environment variables, gh auth status, or hosts.yml")
	}

	if githubEmail == "" {
		out, err := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".email").Output()
		if err == nil {
			email := strings.TrimSpace(string(out))
			if email != "" && email != "null" {
				githubEmail = email
			}
		}
		if githubEmail == "" {
			out, err := exec.CommandContext(ctx, "git", "config", "user.email").Output()
			if err == nil {
				email := strings.TrimSpace(string(out))
				if email != "" {
					githubEmail = email
				}
			}
		}
		if githubEmail == "" {
			githubEmail = fmt.Sprintf("%s@users.noreply.github.com", githubLogin)
		}
	}

	if geminiKey == "" {
		geminiKey = os.Getenv("GEMINI_API_KEY")
		if geminiKey == "" {
			return fmt.Errorf("GEMINI_API_KEY environment variable not set")
		}
	}

	if confirm {
		fmt.Printf("\nOnboarding Configuration:\n")
		fmt.Printf("  GitHub Login: %s\n", githubLogin)
		fmt.Printf("  GitHub Email: %s\n", githubEmail)
		fmt.Printf("  GitHub Token: %s...%s\n", githubToken[:4], githubToken[len(githubToken)-4:])
		fmt.Printf("  Gemini Key:   %s...%s\n", geminiKey[:4], geminiKey[len(geminiKey)-4:])
		fmt.Printf("\nDo you want to proceed with onboarding user '%s'? [y/N]: ", githubLogin)

		var response string
		_, _ = fmt.Scanln(&response)
		response = strings.ToLower(strings.TrimSpace(response))
		if response != "y" && response != "yes" {
			fmt.Println("User onboarding skipped.")
			return nil
		}
	}

	kubeClient, err := clients.NewKubernetesClient()
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	manager := k8s.NewManager(kubeClient)

	targetNamespace := rootFlags.Namespace
	if targetNamespace == "" {
		targetNamespace = githubLogin
	}

	nsClient := manager.Clientset.CoreV1().Namespaces()
	_, err = nsClient.Get(ctx, targetNamespace, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		fmt.Printf("Creating namespace '%s'...\n", targetNamespace)
		_, err = nsClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: targetNamespace,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating namespace %s: %w", targetNamespace, err)
		}
	} else if err != nil {
		return fmt.Errorf("checking namespace %s: %w", targetNamespace, err)
	} else {
		fmt.Printf("Namespace '%s' already exists.\n", targetNamespace)
	}

	policyName := "sandbox-egress-policy"
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: targetNamespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "sandbox",
						Operator: metav1.LabelSelectorOpExists,
					},
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							IPBlock: &networkingv1.IPBlock{
								CIDR: "0.0.0.0/0",
								Except: []string{
									"10.0.0.0/8",
									"172.16.0.0/12",
									"192.168.0.0/16",
									"169.254.0.0/16",
								},
							},
						},
					},
				},
			},
		},
	}

	_, err = manager.Clientset.NetworkingV1().NetworkPolicies(targetNamespace).Get(ctx, policyName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		fmt.Printf("Creating sandbox egress NetworkPolicy in namespace '%s'...\n", targetNamespace)
		_, err = manager.Clientset.NetworkingV1().NetworkPolicies(targetNamespace).Create(ctx, policy, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating network policy in namespace %s: %w", targetNamespace, err)
		}
	} else if err != nil {
		return fmt.Errorf("checking network policy in namespace %s: %w", targetNamespace, err)
	} else {
		fmt.Printf("Sandbox egress NetworkPolicy already exists in namespace '%s'. Updating it...\n", targetNamespace)
		_, err = manager.Clientset.NetworkingV1().NetworkPolicies(targetNamespace).Update(ctx, policy, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("updating network policy in namespace %s: %w", targetNamespace, err)
		}
	}

	data := map[string][]byte{
		KeyGithubToken:  []byte(githubToken),
		KeyGeminiAPIKey: []byte(geminiKey),
		KeyGithubLogin:  []byte(githubLogin),
		KeyGithubEmail:  []byte(githubEmail),
	}

	fmt.Printf("Creating secret '%s' in namespace '%s'...\n", rootFlags.SecretName, targetNamespace)
	if err := manager.UpdateSecret(ctx, targetNamespace, rootFlags.SecretName, data, nil); err != nil {
		return fmt.Errorf("updating secret '%s': %w", rootFlags.SecretName, err)
	}

	fmt.Printf("Successfully onboarded user '%s' in namespace '%s'.\n", githubLogin, targetNamespace)
	return nil
}
