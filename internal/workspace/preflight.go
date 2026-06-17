package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// ─── Narrow interfaces for testability ───────────────────────────────────────
// Each interface captures exactly the methods used by a single preflight check.
// Production: pass the real AWS SDK client (it satisfies the interface implicitly).
// Tests: inject a mock struct or function type.

// DockerPinger checks Docker daemon reachability.
type DockerPinger interface {
	Ping(ctx context.Context) error
}

// DockerPingerFunc is a function that satisfies DockerPinger.
// Use in cmd/deploy.go to wrap the Docker SDK client without coupling
// the workspace package to the Docker SDK.
//
//	pinger := workspace.DockerPingerFunc(func(ctx context.Context) error {
//	    _, err := dockerClient.Ping(ctx)
//	    return err
//	})
type DockerPingerFunc func(ctx context.Context) error

func (f DockerPingerFunc) Ping(ctx context.Context) error { return f(ctx) }

// TFVersionChecker validates the terraform binary version.
type TFVersionChecker interface {
	// Check returns the semver string (e.g. "1.9.0") or a descriptive error.
	Check(ctx context.Context) (string, error)
}

// TFVersionCheckerFunc is a function that satisfies TFVersionChecker.
//
//	checker := workspace.TFVersionCheckerFunc(workspace.CheckTerraformVersion)
type TFVersionCheckerFunc func(ctx context.Context) (string, error)

func (f TFVersionCheckerFunc) Check(ctx context.Context) (string, error) { return f(ctx) }

// STSCallerAPI is a narrow interface for sts:GetCallerIdentity.
// Satisfied by *sts.Client without any wrapper — the concrete type implements
// exactly this method signature.
type STSCallerAPI interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// ECRAuthAPI is a narrow interface for ecr:GetAuthorizationToken.
// Satisfied by *ecr.Client without any wrapper.
type ECRAuthAPI interface {
	GetAuthorizationToken(ctx context.Context, params *ecr.GetAuthorizationTokenInput, optFns ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

// VPCDescribeAPI is a narrow interface for ec2:DescribeVpcs.
// Satisfied by *ec2.Client without any wrapper.
type VPCDescribeAPI interface {
	DescribeVpcs(ctx context.Context, params *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
}

// IPDetector detects the caller's public IP address.
type IPDetector interface {
	DetectPublicIP(ctx context.Context) (string, error)
}

// IPDetectorFunc is a function that satisfies IPDetector.
//
//	detector := workspace.IPDetectorFunc(workspace.CheckIPHTTP)
type IPDetectorFunc func(ctx context.Context) (string, error)

func (f IPDetectorFunc) DetectPublicIP(ctx context.Context) (string, error) { return f(ctx) }

// ─── Preflight ────────────────────────────────────────────────────────────────

// PreflightOptions configures a Preflight checker.
type PreflightOptions struct {
	DockerPinger DockerPinger
	TFChecker    TFVersionChecker
	STSClient    STSCallerAPI
	ECRClient    ECRAuthAPI
	EC2Client    VPCDescribeAPI
	IPDetector   IPDetector
	// CachedCIDR is read from state.json on subsequent deploys (Q9).
	// When non-empty, live IP detection is skipped entirely.
	CachedCIDR  string
	AllowPublic bool // --allow-public: skip detection, use 0.0.0.0/0
}

// Preflight runs all six pre-deployment environment checks.
// It satisfies ports.PreflightChecker.
type Preflight struct {
	opts PreflightOptions
}

// NewPreflight constructs a Preflight with the given options.
func NewPreflight(opts PreflightOptions) *Preflight {
	return &Preflight{opts: opts}
}

// Check runs all six preflight checks. It collects ALL errors before returning —
// it does NOT fail-fast on the first error. This ensures the user sees every
// problem in one pass rather than fix-and-retry cycling.
//
// On success, populates pctx.AWSAccountID and pctx.CallerIP.
func (p *Preflight) Check(ctx context.Context, pctx *types.PipelineContext) error {
	var errs []error
	collect := func(label string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		}
	}

	// Run all six checks regardless of individual failures (aggregate-and-report).
	collect("Docker", p.checkDocker(ctx))
	collect("Terraform", p.checkTerraform(ctx))
	collect("AWS credentials", p.checkAWSIdentity(ctx, pctx))
	collect("ECR authorization", p.checkECRAuth(ctx))
	collect("Default VPC", p.checkDefaultVPC(ctx, pctx))
	collect("Caller IP", p.checkCallerIP(ctx, pctx))

	if len(errs) > 0 {
		return fmt.Errorf("preflight failed (%d error(s)):\n%w", len(errs), errors.Join(errs...))
	}
	return nil
}

func (p *Preflight) checkDocker(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.opts.DockerPinger.Ping(ctx); err != nil {
		return fmt.Errorf("daemon unreachable: %w\n  Is Docker running? Try: docker info", err)
	}
	return nil
}

func (p *Preflight) checkTerraform(ctx context.Context) error {
	_, err := p.opts.TFChecker.Check(ctx)
	return err
}

func (p *Preflight) checkAWSIdentity(ctx context.Context, pctx *types.PipelineContext) error {
	out, err := p.opts.STSClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf(
			"sts:GetCallerIdentity failed: %w\n"+
				"  Check your credentials: aws configure --profile %s",
			err, pctx.AWSProfile,
		)
	}
	pctx.AWSAccountID = aws.ToString(out.Account)
	return nil
}

func (p *Preflight) checkECRAuth(ctx context.Context) error {
	if _, err := p.opts.ECRClient.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{}); err != nil {
		return fmt.Errorf(
			"ecr:GetAuthorizationToken failed: %w\n"+
				"  Your credentials may be read-only. Required IAM action: ecr:GetAuthorizationToken",
			err,
		)
	}
	return nil
}

func (p *Preflight) checkDefaultVPC(ctx context.Context, pctx *types.PipelineContext) error {
	out, err := p.opts.EC2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("isDefault"), Values: []string{"true"}},
		},
	})
	if err != nil {
		return fmt.Errorf("ec2:DescribeVpcs failed: %w", err)
	}
	if len(out.Vpcs) == 0 {
		return fmt.Errorf(
			"no default VPC in region %s\n"+
				"  Re-create: aws ec2 create-default-vpc --region %s\n"+
				"  Custom VPCs are not supported in v1",
			pctx.AWSRegion, pctx.AWSRegion,
		)
	}
	return nil
}

func (p *Preflight) checkCallerIP(ctx context.Context, pctx *types.PipelineContext) error {
	switch {
	case p.opts.AllowPublic:
		pctx.CallerIP = "0.0.0.0/0"
	case p.opts.CachedCIDR != "":
		// Q9: reuse the cached CIDR so the SG rule is stable across IP-rotating deploys.
		pctx.CallerIP = p.opts.CachedCIDR
	default:
		ip, err := p.opts.IPDetector.DetectPublicIP(ctx)
		if err != nil {
			// Q8: hard failure — no silent 0.0.0.0/0 fallback.
			return fmt.Errorf(
				"cannot detect caller IP: %w\n"+
					"  Use --allow-public to explicitly open ingress to 0.0.0.0/0",
				err,
			)
		}
		pctx.CallerIP = ip + "/32"
	}
	return nil
}

// ─── Production implementations ───────────────────────────────────────────────

// CheckIPHTTP implements the IPDetector contract using checkip.amazonaws.com.
// Q8: returns a hard error if unreachable — no silent 0.0.0.0/0 fallback.
func CheckIPHTTP(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://checkip.amazonaws.com", nil)
	if err != nil {
		return "", err
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET checkip.amazonaws.com: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	ip := strings.TrimSpace(string(b))
	if ip == "" {
		return "", fmt.Errorf("empty response from checkip.amazonaws.com")
	}
	return ip, nil
}

// CheckTerraformVersion implements the TFVersionChecker contract.
// Returns an error if terraform is not on PATH or version < 1.5.0.
func CheckTerraformVersion(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "terraform", "version", "-json").Output()
	if err != nil {
		return "", fmt.Errorf(
			"terraform not found on PATH: %w\n"+
				"  Install: https://developer.hashicorp.com/terraform/downloads",
			err,
		)
	}
	var tfv struct {
		TerraformVersion string `json:"terraform_version"`
	}
	if err := json.Unmarshal(out, &tfv); err != nil {
		return "", fmt.Errorf("parse terraform version output: %w", err)
	}
	if tfv.TerraformVersion == "" {
		return "", fmt.Errorf("empty version in terraform -json output")
	}
	if !terraformVersionOK(tfv.TerraformVersion) {
		return "", fmt.Errorf(
			"terraform >= 1.5.0 required, found %s\n"+
				"  Install: https://developer.hashicorp.com/terraform/downloads",
			tfv.TerraformVersion,
		)
	}
	return tfv.TerraformVersion, nil
}

// terraformVersionOK returns true if ver is >= "1.5.0".
// Parses major.minor numerically to handle two-digit minor versions correctly.
func terraformVersionOK(ver string) bool {
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major > 1 {
		return true
	}
	if major < 1 {
		return false
	}
	return minor >= 5
}
