package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/spf13/cobra"

	"github.com/francisco3ferraz/vessel-cli/internal/workspace"
)

var execCmd = &cobra.Command{
	Use:   "exec",
	Short: "Open an interactive shell inside a running ECS Fargate task",
	Long: `exec uses ECS Exec (backed by AWS SSM) to open an interactive shell
directly inside a running container — no SSH, no bastion, no open ports needed.

Prerequisites:
  1. The Session Manager plugin for the AWS CLI must be installed.
     https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html
  2. The app must have been deployed with vessel-cli (which enables ECS Exec
     and adds the required SSM IAM permissions automatically).

Press Ctrl+C to exit the shell.`,
	RunE: runExec,
}

func init() {
	execCmd.Flags().StringP("command", "c", "/bin/sh", "Command to run inside the container")
	execCmd.Flags().StringP("task", "t", "", "Task ARN or ID to exec into (default: first RUNNING task)")
	execCmd.Flags().StringP("environment", "e", "", "Deployment environment (e.g. staging, prod). Reads state.<env>.json.")
	rootCmd.AddCommand(execCmd)
}

func runExec(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	projectDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolve directory: %w", err)
	}

	// Load state — source of truth for cluster/service ARNs and app name.
	projCfg, err := workspace.LoadProjectConfig(projectDir)
	if err != nil {
		return fmt.Errorf("load vessel.json: %w", err)
	}

	environment, _ := cmd.Flags().GetString("environment")
	if environment == "" && projCfg.DefaultEnvironment != "" {
		environment = projCfg.DefaultEnvironment
	}

	stateMgr := workspace.NewStateManager()
	state, err := stateMgr.LoadForEnv(ctx, projectDir, environment, projCfg.RemoteState)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state.AppName == "" {
		return fmt.Errorf(
			"no deployment found in this directory\n" +
				"  Run: vessel-cli deploy",
		)
	}

	// Flags.
	region, _ := cmd.Root().PersistentFlags().GetString("region")
	profile, _ := cmd.Root().PersistentFlags().GetString("profile")
	command, _ := cmd.Flags().GetString("command")
	taskOverride, _ := cmd.Flags().GetString("task")

	if region == "" {
		region = state.AWSRegion
	}

	// Build AWS client.
	awsOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if profile != "" {
		awsOpts = append(awsOpts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsOpts...)
	if err != nil {
		return fmt.Errorf("AWS config: %w", err)
	}

	ecsClient := awsecs.NewFromConfig(cfg)

	// Resolve task ARN — use --task override or discover from the service.
	taskARN := taskOverride
	if taskARN == "" {
		taskARN, err = findRunningTask(ctx, ecsClient, state.ECSClusterARN, state.ECSServiceARN)
		if err != nil {
			return err
		}
	}

	// Print a friendly header.
	shortTask := taskARN
	if idx := strings.LastIndex(taskARN, "/"); idx >= 0 {
		shortTask = taskARN[idx+1:]
	}
	fmt.Fprintf(os.Stderr, "  → Exec into task %s  (Ctrl+C to exit)\n\n", shortTask)

	// Check that the aws CLI is available (SSM plugin must also be installed,
	// but that's only detectable at runtime by the aws CLI itself).
	if _, err := exec.LookPath("aws"); err != nil {
		return fmt.Errorf(
			"aws CLI not found in PATH\n" +
				"  Install it: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html",
		)
	}

	// Build the aws ecs execute-command invocation.
	args := []string{
		"ecs", "execute-command",
		"--cluster", state.ECSClusterARN,
		"--task", taskARN,
		"--container", state.AppName,
		"--command", command,
		"--interactive",
		"--region", region,
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}

	// Hand off: exec replaces stdin/stdout/stderr so the user gets a true
	// interactive terminal exactly as if they'd run the aws CLI directly.
	awsCmd := exec.CommandContext(ctx, "aws", args...)
	awsCmd.Stdin = os.Stdin
	awsCmd.Stdout = os.Stdout
	awsCmd.Stderr = os.Stderr

	if err := awsCmd.Run(); err != nil {
		// Ctrl-C causes a non-zero exit — treat that as a clean exit.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("exec session ended: %w", err)
	}
	return nil
}

// findRunningTask lists tasks for the service and returns the ARN of the first
// RUNNING one. Returns a clear error if the service has no running tasks.
func findRunningTask(ctx context.Context, client *awsecs.Client, clusterARN, serviceARN string) (string, error) {
	// Extract just the service name from the ARN for the ListTasks filter.
	serviceName := arnSuffix(serviceARN)

	list, err := client.ListTasks(ctx, &awsecs.ListTasksInput{
		Cluster:       aws.String(clusterARN),
		ServiceName:   aws.String(serviceName),
		DesiredStatus: "RUNNING",
	})
	if err != nil {
		return "", fmt.Errorf("ecs:ListTasks: %w", err)
	}
	if len(list.TaskArns) == 0 {
		return "", fmt.Errorf(
			"no RUNNING tasks found for this service\n" +
				"  Check status:  vessel-cli status\n" +
				"  View logs:     vessel-cli logs",
		)
	}

	// Describe to confirm at least one is genuinely RUNNING (ListTasks with
	// DesiredStatus=RUNNING can include tasks that are still PROVISIONING).
	described, err := client.DescribeTasks(ctx, &awsecs.DescribeTasksInput{
		Cluster: aws.String(clusterARN),
		Tasks:   list.TaskArns,
	})
	if err != nil {
		return "", fmt.Errorf("ecs:DescribeTasks: %w", err)
	}

	for _, t := range described.Tasks {
		if aws.ToString(t.LastStatus) == "RUNNING" {
			return aws.ToString(t.TaskArn), nil
		}
	}

	return "", fmt.Errorf(
		"tasks found but none are RUNNING yet (they may still be starting)\n" +
			"  Wait a moment and retry, or run: vessel-cli status",
	)
}
