package cmd

import (
	"fmt"
	"os"

	"gemini-configmap-tool/pkg/kube"
	"gemini-configmap-tool/pkg/restorer"

	"github.com/spf13/cobra"
)

var (
	configMapName string
	outputDir     string
)

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore a directory from ConfigMaps",
	Run: func(cmd *cobra.Command, args []string) {
		clientset, err := kube.GetClientset()
		if err != nil {
			fmt.Printf("Error getting Kubernetes clientset: %v\n", err)
			os.Exit(1)
		}

		if err := restorer.RestoreFromConfigMaps(clientset, configMapName, outputDir, namespace); err != nil {
			fmt.Printf("Error restoring from ConfigMaps: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(restoreCmd)
	restoreCmd.Flags().StringVar(&configMapName, "name", "", "The base name of the ConfigMap(s) to restore (required)")
	restoreCmd.MarkFlagRequired("name")
	restoreCmd.Flags().StringVar(&outputDir, "output-dir", "", "The directory to write the files to (required)")
	restoreCmd.MarkFlagRequired("output-dir")
	restoreCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace where the ConfigMaps are located")
}
