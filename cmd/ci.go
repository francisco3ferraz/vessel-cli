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

# Multi-environment deployment pipeline:
#   - Push to 'main' deploys to staging  (vessel-cli deploy --environment staging)
#   - Push a version tag (v*) deploys to prod (vessel-cli deploy --environment prod)
#
# Prerequisites:
#   1. Configure AWS OIDC: https://docs.github.com/en/actions/security-guides/security-hardening-for-github-actions
#   2. Set the following GitHub Secrets / Variables:
#        AWS_ROLE_ARN_STAGING  — IAM role ARN for the staging AWS account/environment
#        AWS_ROLE_ARN_PROD     — IAM role ARN for the prod AWS account/environment
#   3. Set vessel.json remote_state so state is shared with the team (vessel-cli state init)

on:
  push:
    branches:
      - main       # → staging deploy
    tags:
      - "v*"       # → prod deploy

permissions:
  id-token: write  # Required for AWS OIDC authentication
  contents: read

jobs:
  # ── Staging: deploy on every push to main ──────────────────────────────────
  deploy-staging:
    if: github.ref_type == 'branch' && github.ref_name == 'main'
    name: Deploy → staging
    runs-on: ubuntu-latest
    environment: staging  # Maps to a GitHub Environment for protection rules
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.22"

      - name: Install vessel-cli
        run: go install github.com/francisco3ferraz/vessel-cli@latest

      - name: Configure AWS credentials (staging)
        uses: aws-actions/configure-aws-credentials@v4
        with:
          # TODO: Replace with your staging AWS OIDC Role ARN
          role-to-assume: ${{ vars.AWS_ROLE_ARN_STAGING }}
          aws-region: us-east-1

      - name: Deploy to staging
        # --environment namespaces all AWS resources as <app>-staging
        # --yes skips the interactive confirmation prompt
        run: vessel-cli deploy --environment staging --yes

  # ── Prod: deploy on semver tag push (e.g. v1.2.3) ─────────────────────────
  deploy-prod:
    if: github.ref_type == 'tag'
    name: Deploy → prod
    runs-on: ubuntu-latest
    environment: prod  # GitHub Environment: require approvals, secrets, etc.
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.22"

      - name: Install vessel-cli
        run: go install github.com/francisco3ferraz/vessel-cli@latest

      - name: Configure AWS credentials (prod)
        uses: aws-actions/configure-aws-credentials@v4
        with:
          # TODO: Replace with your prod AWS OIDC Role ARN
          role-to-assume: ${{ vars.AWS_ROLE_ARN_PROD }}
          aws-region: us-east-1

      - name: Deploy to prod
        # --tag pins the image to the exact git tag for reproducibility
        run: vessel-cli deploy --environment prod --tag ${{ github.ref_name }} --yes
`
