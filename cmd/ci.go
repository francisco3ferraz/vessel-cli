package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "Manage CI/CD integrations for vessel-cli",
}

var ciGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate a ready-made GitHub Actions deployment workflow",
	RunE: func(cmd *cobra.Command, args []string) error {
		projectDir, err := filepath.Abs(".")
		if err != nil {
			return err
		}

		workflowsDir := filepath.Join(projectDir, ".github", "workflows")
		if err := os.MkdirAll(workflowsDir, 0755); err != nil {
			return fmt.Errorf("create .github/workflows directory: %w", err)
		}

		outPath := filepath.Join(workflowsDir, "vessel-deploy.yml")
		if _, err := os.Stat(outPath); err == nil {
			return fmt.Errorf("file already exists: %s", outPath)
		}

		if err := os.WriteFile(outPath, []byte(githubActionsTemplate), 0644); err != nil {
			return fmt.Errorf("write workflow file: %w", err)
		}

		fmt.Printf("✓ Generated GitHub Actions workflow at %s\n\n", filepath.Join(".github", "workflows", "vessel-deploy.yml"))
		fmt.Println("Next steps:")
		fmt.Println("  1. Update the 'role-to-assume' in the generated YAML with your AWS OIDC Role ARN.")
		fmt.Println("  2. Update the 'aws-region' if you are deploying outside us-east-1.")
		fmt.Println("  3. Commit and push to the 'main' branch to trigger the deployment.")
		
		return nil
	},
}

func init() {
	ciCmd.AddCommand(ciGenerateCmd)
	rootCmd.AddCommand(ciCmd)
}

const githubActionsTemplate = `name: Deploy

on:
  push:
    branches:
      - main

permissions:
  id-token: write # Required for AWS OIDC authentication
  contents: read

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.22"

      - name: Install vessel-cli
        run: go install github.com/francisco3ferraz/vessel-cli@latest

      - name: Configure AWS Credentials
        uses: aws-actions/configure-aws-credentials@v4
        with:
          # TODO: Replace with your AWS OIDC Role ARN
          # See: https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/configuring-openid-connect-in-amazon-web-services
          role-to-assume: arn:aws:iam::123456789012:role/my-github-actions-role
          aws-region: us-east-1

      - name: Deploy Application
        # The --yes flag skips the interactive confirmation prompt
        run: vessel-cli deploy --yes
`
