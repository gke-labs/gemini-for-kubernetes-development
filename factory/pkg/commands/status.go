package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/geminitokens"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewStatusCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Diagnostic pre-flight checks to verify cluster and factory health",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "CHECK\tSTATUS\tMESSAGE")

			kubeClient, err := clients.NewKubernetesClient()
			if err != nil {
				fmt.Fprintf(w, "Kubernetes API\t[FAIL]\t%s\n", err)
				w.Flush()
				return nil
			}
			fmt.Fprintln(w, "Kubernetes API\t[OK]\tConnected to cluster")

			fmt.Fprintf(w, "Namespace\t[OK]\t%s\n", rootFlags.Namespace)

			_, err = kubeClient.Clientset.Discovery().ServerResourcesForGroupVersion("agents.x-k8s.io/v1alpha1")
			if err != nil {
				fmt.Fprintf(w, "Agent CRDs\t[FAIL]\tagents.x-k8s.io/v1alpha1 not found (run 'factory up')\n")
			} else {
				fmt.Fprintln(w, "Agent CRDs\t[OK]\tagents.x-k8s.io/v1alpha1 installed")
			}

			secret, err := kubeClient.Clientset.CoreV1().Secrets(rootFlags.Namespace).Get(ctx, rootFlags.SecretName, metav1.GetOptions{})
			if err != nil {
				fmt.Fprintf(w, "User Secret\t[FAIL]\tSecret '%s' missing in namespace '%s' (run 'factory user onboard')\n", rootFlags.SecretName, rootFlags.Namespace)
			} else {
				if string(secret.Data[KeyGithubLogin]) == "" {
					fmt.Fprintf(w, "GitHub Login\t[FAIL]\tGITHUB_LOGIN missing in secret '%s'\n", rootFlags.SecretName)
				} else {
					fmt.Fprintf(w, "GitHub Login\t[OK]\t%s\n", string(secret.Data[KeyGithubLogin]))
				}
				if string(secret.Data[KeyGithubToken]) == "" {
					fmt.Fprintf(w, "GitHub Token\t[FAIL]\tGITHUB_TOKEN missing in secret '%s'\n", rootFlags.SecretName)
				} else {
					fmt.Fprintf(w, "GitHub Token\t[OK]\tConfigured in secret '%s'\n", rootFlags.SecretName)
				}
				geminiKey := getGeminiAPIKey(secret)
				if geminiKey == "" {
					fmt.Fprintf(w, "Gemini Key\t[FAIL]\tGEMINI_API_KEY missing in secret '%s' and TOKENSCRIPT_DIR was not set or returned empty\n", rootFlags.SecretName)
				} else {
					if token := geminitokens.GetGeminiAPIKey(nil); token != "" {
						fmt.Fprintf(w, "Gemini Key\t[OK]\tConfigured via dynamic tokenscript\n")
						status, err := geminitokens.GetTokensStatus()
						if err == nil && status != nil {
							fmt.Fprintf(w, "Tokens Status\t[OK]\tTotal: %d (Active: %d, Quota Exceeded: %d)\n", status.Total, status.Active, status.QuotaExceeded)
							if len(status.QuotaExceededList) > 0 {
								fmt.Fprintf(w, "Quota Exceeded\t[WARN]\t%s (resets at midnight)\n", strings.Join(status.QuotaExceededList, ", "))
							}
						}
					} else {
						fmt.Fprintf(w, "Gemini Key\t[OK]\tConfigured in secret '%s'\n", rootFlags.SecretName)
					}
				}
			}

			w.Flush()
			return nil
		},
	}
	return cmd
}
