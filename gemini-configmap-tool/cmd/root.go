package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gemini-configmap-tool",
	Short: "A tool to manage .gemini folder as Kubernetes ConfigMaps",
	Long: `A CLI tool to convert a directory into one or more ConfigMaps
and apply them to a Kubernetes cluster. It can also restore the
directory structure from the ConfigMaps.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
