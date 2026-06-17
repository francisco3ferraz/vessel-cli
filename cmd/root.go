// Package cmd contains the cobra CLI entry points for vessel-cli.
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "vessel-cli",
	Short: "Declarative platform engine for Go microservices",
	Long: `vessel-cli deploys Go microservices to AWS ECS Fargate via a deterministic,
idempotent pipeline. It inspects your project, generates a hardened Dockerfile,
provisions cloud infrastructure via Terraform, and deploys your service — all
from a single command.

Run in the root directory of any Go project:
  vessel-cli deploy`,
}

// Execute is the top-level entry point called by main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("region", "r", "us-east-1", "AWS region")
	rootCmd.PersistentFlags().StringP("profile", "p", "default", "AWS profile")
}
