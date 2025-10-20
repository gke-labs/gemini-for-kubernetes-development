package cmd

import (
	"fmt"
	"os"

	"gemini-configmap-tool/pkg/kube"
	"gemini-configmap-tool/pkg/loader"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes/scheme"
)

var (
	directory  string
	outputFile string
	apply      bool
	maxSize    int
	namespace  string
)

var loadCmd = &cobra.Command{
	Use:   "load",
	Short: "Load a directory into one or more ConfigMaps",
	Run: func(cmd *cobra.Command, args []string) {
		if _, err := os.Stat(directory); os.IsNotExist(err) {
			fmt.Printf("Error: directory %s does not exist\n", directory)
			os.Exit(1)
		}

		configMaps, err := loader.CreateConfigMaps(directory, maxSize)
		if err != nil {
			fmt.Printf("Error creating ConfigMaps: %v\n", err)
			os.Exit(1)
		}

		if outputFile != "" {
			serializer := json.NewYAMLSerializer(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme)
			f, err := os.Create(outputFile)
			if err != nil {
				fmt.Printf("Error creating output file: %v\n", err)
				os.Exit(1)
			}
			defer f.Close()

			for _, cm := range configMaps {
				// Need to create a copy of the object to avoid issues with the serializer
				obj := cm.DeepCopyObject()
				if err := serializer.Encode(obj, f); err != nil {
					fmt.Printf("Error writing ConfigMap to file: %v\n", err)
					os.Exit(1)
				}
				// Add separator for multiple documents
				if _, err := f.WriteString("---\n"); err != nil {
					fmt.Printf("Error writing separator to file: %v\n", err)
					os.Exit(1)
				}
			}
			fmt.Printf("Successfully wrote %d ConfigMaps to %s\n", len(configMaps), outputFile)
		}

		if apply {
			clientset, err := kube.GetClientset()
			if err != nil {
				fmt.Printf("Error getting Kubernetes clientset: %v\n", err)
				os.Exit(1)
			}
			for _, cm := range configMaps {
				if err := kube.ApplyConfigMap(clientset, cm, namespace); err != nil {
					fmt.Printf("Error applying ConfigMap %s: %v\n", cm.Name, err)
					os.Exit(1)
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(loadCmd)
	loadCmd.Flags().StringVarP(&directory, "dir", "d", "", "The input directory (required)")
	loadCmd.MarkFlagRequired("dir")
	loadCmd.Flags().StringVarP(&outputFile, "output-file", "o", "", "Path to write the YAML manifest")
	loadCmd.Flags().BoolVar(&apply, "apply", false, "Apply the ConfigMap to the cluster")
	loadCmd.Flags().IntVar(&maxSize, "max-size", 1024*1024, "Maximum size for a single ConfigMap in bytes")
	loadCmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace to apply the ConfigMap to")
}
