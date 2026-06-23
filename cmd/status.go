package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/spf13/cobra"

	"github.com/francisco3ferraz/vessel-cli/internal/workspace"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show deployment status and public address of the current project",
	Long: `status reads .vessel-cli/state.json and queries AWS for the current
task health, public IP address, and a ready-to-run log command.`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	projectDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolve directory: %w", err)
	}

	// Load local state — this is the source of truth for all resource IDs.
	projCfg, err := workspace.LoadProjectConfig(projectDir)
	if err != nil {
		return fmt.Errorf("load vessel.json: %w", err)
	}

	stateMgr := workspace.NewStateManager()
	state, err := stateMgr.Load(ctx, projectDir, projCfg.RemoteState)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state.AppName == "" {
		return fmt.Errorf(
			"no deployment found in this directory\n" +
				"  Run: vessel-cli deploy",
		)
	}

	// Resolve region — flag > state.json > SDK default chain.
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
	ec2Client := ec2.NewFromConfig(cfg)

	// List tasks for the service.
	taskList, err := ecsClient.ListTasks(ctx, &awsecs.ListTasksInput{
		Cluster:     aws.String(state.ECSClusterARN),
		ServiceName: aws.String(arnSuffix(state.ECSServiceARN)),
	})
	if err != nil {
		return fmt.Errorf("ecs:ListTasks: %w", err)
	}

	// ── Print header ─────────────────────────────────────────────────────────
	fmt.Println()
	field := func(k, v string) { fmt.Printf("  %-14s%s\n", k+":", v) }

	field("app", state.AppName)
	field("region", region)
	field("image", state.LastImageTag)
	if state.LastDeployedAt != "" {
		field("deployed", humanAgo(state.LastDeployedAt))
	}

	if len(taskList.TaskArns) == 0 {
		field("status", "NO TASKS  (service may have desired_count=0)")
		field("logs", logTailCmd(state.AppName, region))
		fmt.Println()
		return nil
	}

	// Describe tasks to get status + ENI → public IP.
	described, err := ecsClient.DescribeTasks(ctx, &awsecs.DescribeTasksInput{
		Cluster: aws.String(state.ECSClusterARN),
		Tasks:   taskList.TaskArns,
	})
	if err != nil {
		return fmt.Errorf("ecs:DescribeTasks: %w", err)
	}

	total := len(described.Tasks)
	running := 0
	var publicIP string

	for _, t := range described.Tasks {
		if aws.ToString(t.LastStatus) == "RUNNING" {
			running++
		}
		// Walk attachments to find the ENI and its public IP.
		for _, att := range t.Attachments {
			if aws.ToString(att.Type) != "ElasticNetworkInterface" {
				continue
			}
			for _, d := range att.Details {
				if aws.ToString(d.Name) == "networkInterfaceId" {
					ip, _ := lookupPublicIP(ctx, ec2Client, aws.ToString(d.Value))
					if ip != "" && publicIP == "" {
						publicIP = ip
					}
				}
			}
		}
	}

	// Status line.
	var statusLine string
	switch {
	case running == total:
		statusLine = fmt.Sprintf("✅  RUNNING  (%d/%d tasks)", running, total)
	case running == 0:
		statusLine = fmt.Sprintf("❌  STOPPED  (%d/%d tasks running)", running, total)
	default:
		statusLine = fmt.Sprintf("⚠️   PARTIAL  (%d/%d tasks running)", running, total)
	}
	field("status", statusLine)

	// Address: prefer stable ALB URL; fall back to floating task public IP.
	if state.ALBDNSName != "" {
		scheme := "http"
		if state.CertificateARN != "" {
			scheme = "https"
		}
		field("address", fmt.Sprintf("%s://%s", scheme, state.ALBDNSName))
	} else if publicIP != "" {
		field("address", fmt.Sprintf("http://%s:%d", publicIP, state.Port))
	} else {
		field("address", "(no public IP — task may still be starting)")
	}
	field("logs", logTailCmd(state.AppName, region))
	fmt.Println()

	return nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func lookupPublicIP(ctx context.Context, client *ec2.Client, eniID string) (string, error) {
	out, err := client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	})
	if err != nil || len(out.NetworkInterfaces) == 0 {
		return "", err
	}
	if out.NetworkInterfaces[0].Association == nil {
		return "", nil
	}
	return aws.ToString(out.NetworkInterfaces[0].Association.PublicIp), nil
}

// arnSuffix returns the last segment of an ARN (after the last "/").
// "arn:aws:ecs:us-east-1:123:service/cluster/svc" → "svc"
func arnSuffix(arn string) string {
	parts := strings.Split(arn, "/")
	return parts[len(parts)-1]
}

func logTailCmd(appName, region string) string {
	return fmt.Sprintf("aws logs tail /ecs/%s --follow --region %s", appName, region)
}

func humanAgo(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
