package workspace_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/francisco3ferraz/vessel-cli/pkg/types"
	"github.com/francisco3ferraz/vessel-cli/internal/workspace"
)

// ─── Mock implementations ──────────────────────────────────────────────────────
// Each mock satisfies the narrow interface defined in preflight.go.
// Using struct+function-field pattern so individual test cases can override
// specific behaviour without constructing a full struct per case.

type mockSTSClient struct {
	fn func(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

func (m *mockSTSClient) GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return m.fn(ctx, params, optFns...)
}

type mockECRClient struct {
	fn func(ctx context.Context, params *ecr.GetAuthorizationTokenInput, optFns ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

func (m *mockECRClient) GetAuthorizationToken(ctx context.Context, params *ecr.GetAuthorizationTokenInput, optFns ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	return m.fn(ctx, params, optFns...)
}

type mockEC2Client struct {
	fn func(ctx context.Context, params *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
}

func (m *mockEC2Client) DescribeVpcs(ctx context.Context, params *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return m.fn(ctx, params, optFns...)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// happyOpts returns PreflightOptions where every check succeeds.
func happyOpts() workspace.PreflightOptions {
	return workspace.PreflightOptions{
		DockerPinger: workspace.DockerPingerFunc(func(_ context.Context) error { return nil }),
		TFChecker: workspace.TFVersionCheckerFunc(func(_ context.Context) (string, error) {
			return "1.9.0", nil
		}),
		STSClient: &mockSTSClient{fn: func(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
			return &sts.GetCallerIdentityOutput{
				Account: aws.String("123456789012"),
				Arn:     aws.String("arn:aws:iam::123456789012:user/test"),
				UserId:  aws.String("AIDATEST"),
			}, nil
		}},
		ECRClient: &mockECRClient{fn: func(_ context.Context, _ *ecr.GetAuthorizationTokenInput, _ ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
			return &ecr.GetAuthorizationTokenOutput{}, nil
		}},
		EC2Client: &mockEC2Client{fn: func(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
			return &ec2.DescribeVpcsOutput{
				Vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-12345")}},
			}, nil
		}},
		IPDetector: workspace.IPDetectorFunc(func(_ context.Context) (string, error) {
			return "1.2.3.4", nil
		}),
	}
}

func newPctx() *types.PipelineContext {
	return &types.PipelineContext{
		AWSRegion:  "us-east-1",
		AWSProfile: "default",
	}
}

// assertErr is a tiny helper: verifies err is non-nil and contains the given substring.
func assertErr(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("error %q does not contain %q", err.Error(), contains)
	}
}

// assertNoErr fails the test if err is non-nil.
func assertNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ─── Check 1: Docker ──────────────────────────────────────────────────────────

func TestPreflightDocker_Success(t *testing.T) {
	pf := workspace.NewPreflight(happyOpts())
	assertNoErr(t, pf.Check(context.Background(), newPctx()))
}

func TestPreflightDocker_Failure(t *testing.T) {
	opts := happyOpts()
	opts.DockerPinger = workspace.DockerPingerFunc(func(_ context.Context) error {
		return errors.New("connection refused")
	})
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "Docker")
}

// ─── Check 2: Terraform ───────────────────────────────────────────────────────

func TestPreflightTerraform_Success(t *testing.T) {
	pf := workspace.NewPreflight(happyOpts())
	assertNoErr(t, pf.Check(context.Background(), newPctx()))
}

func TestPreflightTerraform_NotFound(t *testing.T) {
	opts := happyOpts()
	opts.TFChecker = workspace.TFVersionCheckerFunc(func(_ context.Context) (string, error) {
		return "", errors.New("terraform not found on PATH")
	})
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "Terraform")
}

func TestPreflightTerraform_VersionTooOld(t *testing.T) {
	opts := happyOpts()
	opts.TFChecker = workspace.TFVersionCheckerFunc(func(_ context.Context) (string, error) {
		return "", errors.New("terraform >= 1.5.0 required, found 1.4.6")
	})
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "Terraform")
}

// ─── Check 3: AWS identity ────────────────────────────────────────────────────

func TestPreflightAWSIdentity_Success(t *testing.T) {
	pctx := newPctx()
	pf := workspace.NewPreflight(happyOpts())
	assertNoErr(t, pf.Check(context.Background(), pctx))
	if pctx.AWSAccountID != "123456789012" {
		t.Errorf("expected AWSAccountID=123456789012, got %q", pctx.AWSAccountID)
	}
}

func TestPreflightAWSIdentity_Failure(t *testing.T) {
	opts := happyOpts()
	opts.STSClient = &mockSTSClient{fn: func(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
		return nil, errors.New("no credentials configured")
	}}
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "AWS credentials")
}

// ─── Check 4: ECR authorization ───────────────────────────────────────────────

func TestPreflightECRAuth_Success(t *testing.T) {
	pf := workspace.NewPreflight(happyOpts())
	assertNoErr(t, pf.Check(context.Background(), newPctx()))
}

func TestPreflightECRAuth_Failure(t *testing.T) {
	opts := happyOpts()
	opts.ECRClient = &mockECRClient{fn: func(_ context.Context, _ *ecr.GetAuthorizationTokenInput, _ ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
		return nil, errors.New("AccessDeniedException: not authorized")
	}}
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "ECR authorization")
}

// ─── Check 5: Default VPC ─────────────────────────────────────────────────────

func TestPreflightDefaultVPC_Found(t *testing.T) {
	pf := workspace.NewPreflight(happyOpts())
	assertNoErr(t, pf.Check(context.Background(), newPctx()))
}

func TestPreflightDefaultVPC_NotFound(t *testing.T) {
	opts := happyOpts()
	opts.EC2Client = &mockEC2Client{fn: func(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
		return &ec2.DescribeVpcsOutput{Vpcs: []ec2types.Vpc{}}, nil // empty list
	}}
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "Default VPC")
}

func TestPreflightDefaultVPC_APIError(t *testing.T) {
	opts := happyOpts()
	opts.EC2Client = &mockEC2Client{fn: func(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
		return nil, errors.New("RequestExpired: signature expired")
	}}
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "Default VPC")
}

// ─── Check 6: Caller IP ───────────────────────────────────────────────────────

func TestPreflightCallerIP_LiveDetected(t *testing.T) {
	pctx := newPctx()
	pf := workspace.NewPreflight(happyOpts())
	assertNoErr(t, pf.Check(context.Background(), pctx))
	if pctx.CallerIP != "1.2.3.4/32" {
		t.Errorf("expected CallerIP=1.2.3.4/32, got %q", pctx.CallerIP)
	}
}

func TestPreflightCallerIP_DetectionFailure(t *testing.T) {
	opts := happyOpts()
	opts.IPDetector = workspace.IPDetectorFunc(func(_ context.Context) (string, error) {
		return "", errors.New("connection timeout")
	})
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	assertErr(t, err, "Caller IP")
	// Q8: error message must name the escape hatch (--allow-public).
	assertErr(t, err, "--allow-public")
}

func TestPreflightCallerIP_CachedCIDRSkipsDetection(t *testing.T) {
	// Q9: when cachedCIDR is set, live detection must NOT be called.
	opts := happyOpts()
	opts.CachedCIDR = "5.6.7.8/32"
	opts.IPDetector = workspace.IPDetectorFunc(func(_ context.Context) (string, error) {
		return "", errors.New("should not have been called")
	})
	pctx := newPctx()
	pf := workspace.NewPreflight(opts)
	assertNoErr(t, pf.Check(context.Background(), pctx))
	if pctx.CallerIP != "5.6.7.8/32" {
		t.Errorf("expected cached CIDR 5.6.7.8/32, got %q", pctx.CallerIP)
	}
}

func TestPreflightCallerIP_AllowPublicSkipsDetection(t *testing.T) {
	// --allow-public must skip live detection and set CallerIP to 0.0.0.0/0.
	opts := happyOpts()
	opts.AllowPublic = true
	opts.IPDetector = workspace.IPDetectorFunc(func(_ context.Context) (string, error) {
		return "", errors.New("should not have been called")
	})
	pctx := newPctx()
	pf := workspace.NewPreflight(opts)
	assertNoErr(t, pf.Check(context.Background(), pctx))
	if pctx.CallerIP != "0.0.0.0/0" {
		t.Errorf("expected CallerIP=0.0.0.0/0 with --allow-public, got %q", pctx.CallerIP)
	}
}

// ─── Aggregate error collection ───────────────────────────────────────────────

func TestPreflightAll_AggregatesErrors(t *testing.T) {
	// All six checks fail. Verify: (a) Check returns a non-nil error,
	// (b) the error mentions all six failing checks, and (c) none of the
	// other checks are skipped due to an earlier failure (aggregate, not fail-fast).
	opts := workspace.PreflightOptions{
		DockerPinger: workspace.DockerPingerFunc(func(_ context.Context) error {
			return errors.New("docker down")
		}),
		TFChecker: workspace.TFVersionCheckerFunc(func(_ context.Context) (string, error) {
			return "", errors.New("terraform not found")
		}),
		STSClient: &mockSTSClient{fn: func(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
			return nil, errors.New("no creds")
		}},
		ECRClient: &mockECRClient{fn: func(_ context.Context, _ *ecr.GetAuthorizationTokenInput, _ ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
			return nil, errors.New("access denied")
		}},
		EC2Client: &mockEC2Client{fn: func(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
			return nil, errors.New("no default vpc")
		}},
		IPDetector: workspace.IPDetectorFunc(func(_ context.Context) (string, error) {
			return "", errors.New("ip detection failed")
		}),
	}
	pf := workspace.NewPreflight(opts)
	err := pf.Check(context.Background(), newPctx())
	if err == nil {
		t.Fatal("expected aggregate error, got nil")
	}

	errStr := err.Error()
	for _, label := range []string{"Docker", "Terraform", "AWS credentials", "ECR authorization", "Default VPC", "Caller IP"} {
		if !strings.Contains(errStr, label) {
			t.Errorf("aggregate error should mention %q\nfull error: %s", label, errStr)
		}
	}
}
