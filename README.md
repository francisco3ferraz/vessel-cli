# vessel-cli

A zero-config CLI that instantly deploys Go applications to AWS ECS Fargate using Docker BuildKit and Terraform. 🚀

`vessel-cli` is an opinionated deployment tool that takes your local Go project and ships it directly to AWS ECS Fargate in minutes. It completely abstracts away the AWS console, automatically handling Docker builds, ECR pushes, and Terraform infrastructure generation (VPC, IAM, Security Groups, Load Balancing) with a single command. 

## Features

- **Zero-config Deployments:** Point it at a Go project, and `vessel-cli` does the rest. It detects your module name, Go version, and sets up the infrastructure.
- **Reproducible Image Tags:** By default, it uses the current `git HEAD` SHA as the Docker image tag, ensuring complete traceability. It enforces a clean git working tree to prevent uncommitted code from being deployed.
- **Multi-Environment Namespacing:** Use the `--environment` (`-e`) flag to isolate `staging` and `prod` deployments. All AWS resources and Terraform states are cleanly namespaced.
- **AWS Secrets Manager Integration:** Pass sensitive data securely with `--secret KEY=VALUE`. Secrets are pushed to AWS Secrets Manager and dynamically injected into the running container—they are never persisted in local state files.
- **ALB & HTTPS Support:** Optionally provision an Application Load Balancer (`--load-balancer`) and bind an ACM certificate (`--certificate-arn`) for production-grade HTTPS endpoints.
- **Team Collaboration (Remote State):** Share deployment state across your team using an S3 backend for Terraform and a `vessel.json` config file.
- **CI/CD Ready:** Generate a complete GitHub Actions deployment pipeline instantly using `vessel-cli ci generate`.

## Installation

```bash
go install github.com/francisco3ferraz/vessel-cli@latest
```

*Requirements:*
- `aws` CLI installed and configured with credentials
- `docker` running locally
- `terraform` CLI installed (v1.x+)
- `git`

## Usage

### 1. Deploy Your App

Navigate to any Go project with a `main.go` and run:

```bash
vessel-cli deploy --allow-public
```

By default, the CLI secures your application by only allowing ingress traffic from your current public IP. Use `--allow-public` to open it up to the internet (`0.0.0.0/0`).

**Options:**
- `--environment staging`: Namespace all resources and state for staging/prod.
- `--env KEY=VALUE`: Inject plain-text environment variables.
- `--secret DB_PASS=hunter2`: Securely inject secrets via AWS Secrets Manager.
- `--load-balancer`: Provision an ALB for a stable URL.
- `--cpu 512 --memory 1024`: Customize Fargate container sizing.
- `--dry-run`: Preview what Terraform will create or change without applying it.

### 2. View Status, Logs, and Exec

To view the public endpoint, running tasks, and deployment state:
```bash
vessel-cli status -e staging
```

To tail live CloudWatch logs directly from the running containers:
```bash
vessel-cli logs -e staging
```

To open an interactive shell inside the running container (using ECS Exec):
```bash
vessel-cli exec -e staging -c /bin/sh
```

### 3. Team Collaboration & Remote State

By default, vessel-cli stores deployment metadata in `.vessel-cli/state.json`. To collaborate with a team, you should use S3 remote state:

```bash
vessel-cli deploy --state-bucket my-terraform-states --state-table my-lock-table
```
This saves the configuration to a `vessel.json` file in your project root, which you should commit to version control. Subsequent deploys by any team member will use the shared S3 state.

### 4. CI/CD Integration

Generate a ready-made, multi-environment GitHub Actions pipeline that deploys to staging on pushes to `main`, and to production on semantic version tags:

```bash
vessel-cli ci generate
```

### 5. Teardown

When you're done, clean up all cloud resources (including ECR images, ECS services, Secrets, and Terraform state) with:

```bash
vessel-cli deploy --destroy -e staging
```

## How It Works

Behind the scenes, `vessel-cli` manages a hidden `.vessel-cli/` directory in your project containing:
- A hardened, multi-stage, distroless `Dockerfile`
- Generated `*.tf` Terraform manifests isolated by environment
- A `state.json` file keeping track of your environment variables and AWS ARNs

The CLI automatically adds `.vessel-cli/tf*` and `.vessel-cli/state*.json` to your `.gitignore`.
