package ecr

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
)

// Cleaner implements ports.ECRCleaner using the AWS ECR SDK.
type Cleaner struct {
	region  string
	profile string
}

// NewCleaner returns a Cleaner for the given region and profile.
func NewCleaner(region, profile string) *Cleaner {
	return &Cleaner{region: region, profile: profile}
}

// compile-time interface guard
var _ ports.ECRCleaner = &Cleaner{}

// DeleteAllImages removes every image from the named ECR repository in batches
// of 100 (the ECR API maximum). It is idempotent: a missing repository or an
// already-empty repository both return nil.
func (c *Cleaner) DeleteAllImages(ctx context.Context, region, repoName string) error {
	if region == "" {
		region = c.region
	}

	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if c.profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("ECR cleaner: AWS config: %w", err)
	}

	client := ecr.NewFromConfig(cfg)

	// Page through all image IDs in the repository.
	var imageIDs []ecrtypes.ImageIdentifier
	paginator := ecr.NewListImagesPaginator(client, &ecr.ListImagesInput{
		RepositoryName: aws.String(repoName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			// If the repository doesn't exist, treat as already clean.
			var notFound *ecrtypes.RepositoryNotFoundException
			if errors.As(err, &notFound) {
				return nil
			}
			return fmt.Errorf("list ECR images in %s: %w", repoName, err)
		}
		imageIDs = append(imageIDs, page.ImageIds...)
	}

	if len(imageIDs) == 0 {
		return nil // already empty
	}

	// Delete in batches of 100 (ECR API limit per request).
	const batchSize = 100
	for i := 0; i < len(imageIDs); i += batchSize {
		end := i + batchSize
		if end > len(imageIDs) {
			end = len(imageIDs)
		}
		batch := imageIDs[i:end]

		out, err := client.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{
			RepositoryName: aws.String(repoName),
			ImageIds:       batch,
		})
		if err != nil {
			return fmt.Errorf("delete ECR images in %s: %w", repoName, err)
		}
		// BatchDeleteImage returns partial failures; treat any as fatal.
		if len(out.Failures) > 0 {
			f := out.Failures[0]
			return fmt.Errorf(
				"delete ECR image %s in %s: %s",
				aws.ToString(f.ImageId.ImageTag),
				repoName,
				aws.ToString(f.FailureReason),
			)
		}
	}

	return nil
}
