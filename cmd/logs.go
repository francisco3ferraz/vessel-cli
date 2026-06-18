package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/spf13/cobra"

	"github.com/francisco3ferraz/vessel-cli/internal/workspace"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Tail live CloudWatch logs for the deployed app",
	Long: `logs streams CloudWatch log events for the current project's ECS tasks.

It reads .vessel-cli/state.json to find the log group (/ecs/<appname>),
then polls for new events and prints them in real time — no need to remember
the log group name or region.

Press Ctrl+C to stop.`,
	RunE: runLogs,
}

func init() {
	logsCmd.Flags().StringP("since", "s", "5m", "Show logs from this duration ago (e.g. 5m, 1h, 24h)")
	logsCmd.Flags().Bool("no-follow", false, "Print recent logs and exit (don't follow)")
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	projectDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolve directory: %w", err)
	}

	// Load state — source of truth for log group and region.
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

	// Flags.
	region, _ := cmd.Root().PersistentFlags().GetString("region")
	profile, _ := cmd.Root().PersistentFlags().GetString("profile")
	since, _ := cmd.Flags().GetString("since")
	noFollow, _ := cmd.Flags().GetBool("no-follow")

	if region == "" {
		region = state.AWSRegion
	}

	// Parse --since duration.
	sinceDur, err := time.ParseDuration(since)
	if err != nil {
		return fmt.Errorf("invalid --since %q: must be a Go duration like 5m, 1h, 24h", since)
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

	client := cloudwatchlogs.NewFromConfig(cfg)
	logGroup := "/ecs/" + state.AppName

	fmt.Fprintf(os.Stderr, "  → Tailing %s  (Ctrl+C to stop)\n\n", logGroup)

	startMs := time.Now().Add(-sinceDur).UnixMilli()
	return tailLogs(ctx, client, logGroup, startMs, noFollow)
}

// tailLogs polls FilterLogEvents in a loop, printing new events as they arrive.
// It stops when ctx is cancelled (Ctrl+C) or, if noFollow is true, when there
// are no more events for the current window.
func tailLogs(ctx context.Context, client *cloudwatchlogs.Client, logGroup string, startMs int64, noFollow bool) error {
	const pollInterval = 2 * time.Second

	// nextToken tracks pagination within a single poll window.
	// startTime advances past the last event we've seen.
	var nextToken *string
	lastEventMs := startMs

	for {
		out, err := client.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName: aws.String(logGroup),
			StartTime:    aws.Int64(lastEventMs),
			NextToken:    nextToken,
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean exit on Ctrl+C
			}
			return fmt.Errorf("cloudwatchlogs:FilterLogEvents: %w", err)
		}

		for _, ev := range out.Events {
			ts := time.UnixMilli(aws.ToInt64(ev.Timestamp)).UTC().Format("2006-01-02T15:04:05Z")
			fmt.Printf("%s  %s\n", ts, aws.ToString(ev.Message))
			// Advance past this event on the next poll (+1ms avoids re-reading the same event).
			if aws.ToInt64(ev.Timestamp) >= lastEventMs {
				lastEventMs = aws.ToInt64(ev.Timestamp) + 1
			}
		}

		// If there are more pages in this poll window, continue paging.
		if out.NextToken != nil {
			nextToken = out.NextToken
			continue
		}

		// Reached end of current window.
		nextToken = nil

		if noFollow {
			return nil
		}

		// Follow mode: wait, then poll again.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
	}
}
