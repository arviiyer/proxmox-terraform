# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Project Does

A Go web portal that provides an EC2-like interface for provisioning VMs on Proxmox using Terraform. Users fill out a web form; the portal generates a Terraform vars file and runs `terraform apply` against the `infra/` directory.

## Running and Building

```bash
# Run the portal (serves on :8088)
cd portal && go run main.go

# Build binary
cd portal && go build -o portal
```

No external Go dependencies — stdlib only. Requires Terraform >= 1.5.0 and the bpg/proxmox provider to be installed for the infra to work.

## Architecture

Two components:

**`portal/`** — Go HTTP server (no frameworks)
- `main.go`: Handles `GET /` (render form) and `POST /launch` (provision VMs). Uses `sync.Mutex` (`applyLock`) to prevent concurrent Terraform applies against shared state.
- `internal/terraform/runner.go`: Wraps `terraform init`, `apply`, `output`, and `refresh-only` via `os/exec`. Writes `portal.auto.tfvars.json` (0600 permissions) before each apply. Default timeout: 20 minutes.
- `config.json`: Defines allowed templates (name → VMID) and instance types. All user inputs are validated against this allowlist before Terraform runs.
- `web/templates/`: `index.html` (launch form), `result.html` (shows ANSI-stripped Terraform logs + provisioned instance table).

**`infra/`** — Terraform configuration for Proxmox
- `main.tf`: Defines EC2-style flavor locals (general/compute/memory × nano–xlarge) and a `proxmox_virtual_environment_vm` resource with `for_each`.
- `variables.tf`: All configurable inputs (VMIDs, template, node, bridge, cloud-init user, SSH key, datastore, clone mode).
- `outputs.tf`: Returns provisioned instances filtered to `10.*` IPs.
- `portal.auto.tfvars.json`: Auto-generated at runtime by the portal — do not edit manually.

## Environment

`SSH_PUBLIC_KEY` env var must be set — the server fatals at startup if missing. It is passed to Terraform as `ssh_public_key` on every apply.

## Key Behaviors

- The portal validates templates and instance types against `config.json` allowlists before doing anything.
- VMID range: 100–999999; count: 1–50.
- Before each apply, existing state is read via `terraform output -json`. New VMs are checked for name and VMID conflicts against existing state, then merged in — so prior VMs are never destroyed by a new launch.
- A `terraform apply -refresh-only` pass runs after provisioning to discover DHCP-assigned IPs.
- Terraform state lives in `infra/terraform.tfstate` (not remote).

## Tests

```bash
cd portal && go test ./...
```

Tests cover `extractVMsFromOutput` (parses terraform output JSON into a name→vmid map) and `mergeVMs` (conflict detection and merge logic).
