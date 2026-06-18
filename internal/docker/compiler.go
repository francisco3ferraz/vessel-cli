package docker

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// Compiler implements ports.DockerCompiler.
type Compiler struct{}

// NewCompiler returns a new Compiler.
func NewCompiler() *Compiler { return &Compiler{} }

// Build runs `docker build` and streams every output line as a BuildEvent.
// Sets pctx.ImageID on success.
func (c *Compiler) Build(ctx context.Context, pctx *types.PipelineContext, events chan<- ports.BuildEvent) error {
	cmd := exec.CommandContext(ctx, "docker",
		"build",
		"--progress=plain",
		"-f", pctx.DockerfilePath,
		"-t", pctx.ImageTag,
		pctx.ProjectDir,
	)

	stdoutW := newLineWriter(events, ports.BuildEventStep)
	stderrW := newLineWriter(events, ports.BuildEventLog)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	runErr := cmd.Run()
	stdoutW.flush()
	stderrW.flush()

	if runErr != nil {
		return fmt.Errorf("docker build: %w", runErr)
	}

	// Best-effort: retrieve the full content-addressable image ID.
	if id, err := resolveImageID(ctx, pctx.ImageTag); err == nil {
		pctx.ImageID = id
	}

	events <- ports.BuildEvent{Type: ports.BuildEventDone, Message: pctx.ImageTag}
	return nil
}

// Push authenticates with ECR, skips if the image tag already exists,
// tags the local image, and pushes to ECR. Streams push progress as BuildEvents.
//
// Requires pctx.CloudOutputs.ECRRepositoryURI to be set (populated by
// the terraform executor in Stage 5).
func (c *Compiler) Push(ctx context.Context, pctx *types.PipelineContext, events chan<- ports.BuildEvent) error {
	repoURI := pctx.CloudOutputs.ECRRepositoryURI
	if repoURI == "" {
		return fmt.Errorf("ECRRepositoryURI is empty — terraform apply must complete before push")
	}

	// Extract the tag suffix from the local image tag (everything after ":").
	tagSuffix := imageTagSuffix(pctx.ImageTag)

	// Full ECR image reference: "<registry>/<repo>:<tag>"
	ecrRef := repoURI + ":" + tagSuffix

	// Derive the bare registry host (everything before the first "/").
	registry := strings.SplitN(repoURI, "/", 2)[0]

	// 1. Get ECR auth token.
	events <- ports.BuildEvent{Type: ports.BuildEventPush, Message: "Authenticating with ECR"}
	password, err := ecrAuthToken(ctx, pctx.AWSRegion, pctx.AWSProfile)
	if err != nil {
		return fmt.Errorf("get ECR auth token: %w", err)
	}

	// 2. docker login (password via stdin to avoid shell history exposure).
	loginCmd := exec.CommandContext(ctx, "docker", "login",
		"--username", "AWS",
		"--password-stdin",
		registry,
	)
	loginCmd.Stdin = strings.NewReader(password)
	if out, err := loginCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker login to %s: %w\n%s", registry, err, string(out))
	}

	// 3. Skip push if the exact tag already exists in ECR (idempotency — Q3 pattern).
	repoName := ecrRepoName(repoURI)
	if ecrTagExists(ctx, pctx.AWSRegion, pctx.AWSProfile, repoName, tagSuffix) {
		events <- ports.BuildEvent{
			Type:    ports.BuildEventPush,
			Message: fmt.Sprintf("Tag %s already in ECR — skipping push", tagSuffix),
		}
		return nil
	}

	// 4. Tag the local image with the full ECR reference.
	tagCmd := exec.CommandContext(ctx, "docker", "tag", pctx.ImageTag, ecrRef)
	if out, err := tagCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker tag %s → %s: %w\n%s", pctx.ImageTag, ecrRef, err, string(out))
	}

	// 5. Push and stream output.
	events <- ports.BuildEvent{Type: ports.BuildEventPush, Message: fmt.Sprintf("Pushing %s", ecrRef)}
	pushCmd := exec.CommandContext(ctx, "docker", "push", ecrRef)
	stdoutW := newLineWriter(events, ports.BuildEventPush)
	stderrW := newLineWriter(events, ports.BuildEventLog)
	pushCmd.Stdout = stdoutW
	pushCmd.Stderr = stderrW

	pushErr := pushCmd.Run()
	stdoutW.flush()
	stderrW.flush()
	if pushErr != nil {
		return fmt.Errorf("docker push %s: %w", ecrRef, pushErr)
	}

	events <- ports.BuildEvent{Type: ports.BuildEventDone, Message: ecrRef}
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// ecrAuthToken fetches the ECR auth token and returns the decoded password.
// The token format from AWS is base64("AWS:<password>").
func ecrAuthToken(ctx context.Context, region, profile string) (string, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return "", fmt.Errorf("load AWS config: %w", err)
	}

	client := ecr.NewFromConfig(cfg)
	out, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return "", fmt.Errorf("GetAuthorizationToken: %w", err)
	}
	if len(out.AuthorizationData) == 0 {
		return "", fmt.Errorf("ECR returned empty authorization data")
	}

	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(out.AuthorizationData[0].AuthorizationToken))
	if err != nil {
		return "", fmt.Errorf("decode ECR token: %w", err)
	}

	// Token format: "AWS:<password>"
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected ECR token format (want AWS:<password>)")
	}
	return parts[1], nil
}

// ecrTagExists returns true if the given tag already exists in ECR.
// Returns false on any error (safe default: proceed with push).
func ecrTagExists(ctx context.Context, region, profile, repoName, tag string) bool {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return false
	}
	client := ecr.NewFromConfig(cfg)
	_, err = client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repoName),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
	})
	return err == nil
}

// resolveImageID returns the full SHA256 image ID for the given local tag.
func resolveImageID(ctx context.Context, tag string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format={{.Id}}", tag).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// imageTagSuffix extracts the tag portion from a local image name.
// "my-app:abc1234" → "abc1234".
// "my-app" → "my-app" (no colon: treat whole string as tag, not "latest").
func imageTagSuffix(imageTag string) string {
	parts := strings.SplitN(imageTag, ":", 2)
	if len(parts) == 2 && parts[1] != "" {
		return parts[1]
	}
	return imageTag // no colon → the full string is the tag
}

// ecrRepoName extracts the repository name from a full ECR URI.
// "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app" → "my-app"
func ecrRepoName(repoURI string) string {
	parts := strings.SplitN(repoURI, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return repoURI
}
