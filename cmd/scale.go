package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/spf13/cobra"

	"github.com/francisco3ferraz/vessel-cli/internal/workspace"
)

var scaleCmd = &cobra.Command{
	Use:   "scale <count>",
	Short: "Set the number of running ECS Fargate tasks",
	Long: `scale changes the ECS service desired_count immediately — no Docker build,
no Terraform apply, no image push. Use it to scale up, scale down, or pause
(scale 0) your service without a full re-deploy.

Examples:
  vessel-cli scale 3    # run 3 replicas
  vessel-cli scale 1    # back to a single task
  vessel-cli scale 0    # pause the service (no cost for tasks; infra stays up)`,
	Args: cobra.ExactArgs(1),
	RunE: runScale,
}

func init() {
	rootCmd.AddCommand(scaleCmd)
}

func runScale(cmd *cobra.Command, args []string) error {
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 0 {
		return fmt.Errorf("invalid count %q: must be a non-negative integer", args[0])
	}

	ctx := context.Background()

	projectDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolve directory: %w", err)
	}

	// Load state — source of truth for cluster/service ARNs.
	stateMgr := workspace.NewStateManager()
	state, err := stateMgr.Load(projectDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state.AppName == "" {
		return fmt.Errorf(
			"no deployment found in this directory\n" +
				"  Run: vessel-cli deploy",
		)
	}
	if state.ECSClusterARN == "" || state.ECSServiceARN == "" {
		return fmt.Errorf(
			"state.json is missing ECS ARNs — run vessel-cli deploy first",
		)
	}

	// Flags.
	region, _ := cmd.Root().PersistentFlags().GetString("region")
	profile, _ := cmd.Root().PersistentFlags().GetString("profile")
	if region == "" {
		region = state.AWSRegion
	}

	awsOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if profile != "" {
		awsOpts = append(awsOpts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsOpts...)
	if err != nil {
		return fmt.Errorf("AWS config: %w", err)
	}

	ecsClient := awsecs.NewFromConfig(cfg)

	// Fetch current desired_count so we can show a before → after line.
	current, err := currentDesiredCount(ctx, ecsClient, state.ECSClusterARN, state.ECSServiceARN)
	if err != nil {
		return err
	}

	if current == int32(count) {
		fmt.Fprintf(os.Stderr, "  → %s already has desired_count=%d — nothing to do.\n",
			state.AppName, count)
		return nil
	}

	// Apply the new desired count.
	_, err = ecsClient.UpdateService(ctx, &awsecs.UpdateServiceInput{
		Cluster:      aws.String(state.ECSClusterARN),
		Service:      aws.String(arnSuffix(state.ECSServiceARN)),
		DesiredCount: aws.Int32(int32(count)),
	})
	if err != nil {
		return fmt.Errorf("ecs:UpdateService: %w", err)
	}

	action := scaleSummary(int(current), count)
	fmt.Printf("  ✓ %s  %s: %d → %d tasks\n", state.AppName, action, current, count)

	if count == 0 {
		fmt.Println("    Service paused. Infrastructure still running — run vessel-cli deploy to resume.")
	} else if count > int(current) {
		fmt.Printf("    ECS is launching %d new task(s). Run vessel-cli status to check.\n", count-int(current))
	} else {
		fmt.Printf("    ECS is draining %d task(s). Run vessel-cli status to check.\n", int(current)-count)
	}

	return nil
}

// currentDesiredCount fetches the current desired_count for the service.
func currentDesiredCount(ctx context.Context, client *awsecs.Client, clusterARN, serviceARN string) (int32, error) {
	out, err := client.DescribeServices(ctx, &awsecs.DescribeServicesInput{
		Cluster:  aws.String(clusterARN),
		Services: []string{arnSuffix(serviceARN)},
	})
	if err != nil {
		return 0, fmt.Errorf("ecs:DescribeServices: %w", err)
	}
	if len(out.Services) == 0 {
		return 0, fmt.Errorf("service not found — run vessel-cli deploy to create it")
	}
	return out.Services[0].DesiredCount, nil
}

// scaleSummary returns a short human label for the transition.
func scaleSummary(from, to int) string {
	switch {
	case to == 0:
		return "paused"
	case from == 0:
		return "resumed"
	case to > from:
		return "scaled up"
	default:
		return "scaled down"
	}
}
