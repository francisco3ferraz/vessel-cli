// Package ecs implements the Deployer port.
// It wraps the AWS ECS service for the single operation vessel-cli needs:
// setting desired_count=1 after the first image push and waiting for stability.
package ecs

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
)

const (
	// defaultScaleTimeout is how long we wait for the service to stabilise.
	// First task pull + start is typically 60-90 s; 5 min is generous but bounded.
	defaultScaleTimeout = 5 * time.Minute
)

// Deployer implements ports.Deployer.
type Deployer struct {
	region  string
	profile string
	timeout time.Duration // overrideable for tests
}

// compile-time interface guard
var _ ports.Deployer = &Deployer{}

// NewDeployer returns a Deployer configured for the given region and profile.
// Pass an empty profile to use the default credential chain.
func NewDeployer(region, profile string) *Deployer {
	return &Deployer{
		region:  region,
		profile: profile,
		timeout: defaultScaleTimeout,
	}
}

// Scale calls ecs:UpdateService to set desired_count and then blocks until the
// service reaches a stable state or the context / timeout expires.
//
// Passing desired=1 on a service already at 1 is idempotent — ECS accepts
// the call and the waiter returns immediately once it confirms stability.
func (d *Deployer) Scale(ctx context.Context, clusterARN, serviceARN string, desired int32) error {
	client, err := d.ecsClient(ctx)
	if err != nil {
		return err
	}

	if _, err := client.UpdateService(ctx, &awsecs.UpdateServiceInput{
		Cluster:      aws.String(clusterARN),
		Service:      aws.String(serviceARN),
		DesiredCount: aws.Int32(desired),
	}); err != nil {
		return fmt.Errorf("ecs:UpdateService: %w", err)
	}

	// Block until the service is stable (running == desired, 0 pending deployments).
	waiter := awsecs.NewServicesStableWaiter(client)
	if err := waiter.Wait(ctx,
		&awsecs.DescribeServicesInput{
			Cluster:  aws.String(clusterARN),
			Services: []string{serviceARN},
		},
		d.timeout,
	); err != nil {
		return fmt.Errorf(
			"waiting for service stability (timeout %s): %w\n"+
				"  The service was updated but the task may still be starting.\n"+
				"  Check the ECS console or re-run `vessel-cli deploy` to retry.",
			d.timeout, err,
		)
	}

	return nil
}

// ecsClient loads the AWS config and returns an ECS client.
func (d *Deployer) ecsClient(ctx context.Context) (*awsecs.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(d.region),
	}
	if d.profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(d.profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return awsecs.NewFromConfig(cfg), nil
}
