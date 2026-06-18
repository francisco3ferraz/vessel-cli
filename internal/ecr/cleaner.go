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

// DeleteAllImages removes every image from the named ECR repository.
//
// BuildKit with --platform flags creates a two-level manifest structure:
//
//	manifest list (tagged, e.g. "local") → references individual image manifests
//	                                        (untagged, digest-only)
//
// ECR refuses to delete a child manifest while its parent manifest list still
// exists ("Requested image referenced by manifest list"). We therefore delete
// in two passes:
//  1. Tagged images  — these are the manifest list roots; deleting them
//     unblocks the children.
//  2. Untagged digests — now free to be deleted.
//
// Idempotent: a missing repository or an already-empty repository return nil.
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

	// List ALL image identifiers (tagged + untagged).
	var tagged, untagged []ecrtypes.ImageIdentifier
	paginator := ecr.NewListImagesPaginator(client, &ecr.ListImagesInput{
		RepositoryName: aws.String(repoName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			var notFound *ecrtypes.RepositoryNotFoundException
			if errors.As(err, &notFound) {
				return nil // repo already gone — nothing to do
			}
			return fmt.Errorf("list ECR images in %s: %w", repoName, err)
		}
		for _, id := range page.ImageIds {
			// Images with a tag are manifest list roots (created by BuildKit).
			// Images without a tag are child manifests referenced by the list.
			if aws.ToString(id.ImageTag) != "" {
				tagged = append(tagged, id)
			} else {
				untagged = append(untagged, id)
			}
		}
	}

	if len(tagged)+len(untagged) == 0 {
		return nil // already empty
	}

	// Pass 1: delete manifest lists (tagged). This unblocks the child manifests.
	// Pass 2: delete remaining child manifests (untagged).
	for _, batch := range [][]ecrtypes.ImageIdentifier{tagged, untagged} {
		if err := deleteBatched(ctx, client, repoName, batch); err != nil {
			return err
		}
	}
	return nil
}

// deleteBatched sends imageIDs to BatchDeleteImage in chunks of 100.
// It ignores "referenced by manifest list" failures that can occur if a
// manifest list was already deleted in a previous partial run.
func deleteBatched(ctx context.Context, client *ecr.Client, repoName string, imageIDs []ecrtypes.ImageIdentifier) error {
	const batchSize = 100
	for i := 0; i < len(imageIDs); i += batchSize {
		end := i + batchSize
		if end > len(imageIDs) {
			end = len(imageIDs)
		}
		out, err := client.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{
			RepositoryName: aws.String(repoName),
			ImageIds:       imageIDs[i:end],
		})
		if err != nil {
			return fmt.Errorf("delete ECR images in %s: %w", repoName, err)
		}
		for _, f := range out.Failures {
			// "REFERENCED_BY_MANIFEST_LIST" means the manifest list that referenced
			// this digest was already deleted in a prior pass or run — safe to skip.
			if f.FailureCode == ecrtypes.ImageFailureCodeImageReferencedByManifestList {
				continue
			}
			ref := aws.ToString(f.ImageId.ImageDigest)
			if tag := aws.ToString(f.ImageId.ImageTag); tag != "" {
				ref = tag
			}
			return fmt.Errorf("delete ECR image %q in %s: %s", ref, repoName, aws.ToString(f.FailureReason))
		}
	}
	return nil
}
