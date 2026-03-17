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

All three env vars are required — the server fatals at startup if any are missing:
```bash
PVE_ENDPOINT="https://your-proxmox-host"   # no :8006 if behind a reverse proxy
PVE_API_TOKEN="user@pam!tokenid=secret"
SSH_PUBLIC_KEY="ssh-ed25519 ..."
```

No external Go dependencies — stdlib only. Requires Terraform >= 1.5.0 and the bpg/proxmox provider.

## Architecture

Two components:

**`portal/`** — Go HTTP server (no frameworks)
- `main.go`: Handles `GET /` (instance listing + launch form), `POST /launch` (provision), `POST /destroy` (terminate), `POST /start`, `POST /stop`, `POST /toggle-protection` (lock/unlock). Uses `sync.Mutex` (`applyLock`) to prevent concurrent Terraform applies.
- `internal/terraform/runner.go`: Wraps `terraform init`, `apply`, `apply -destroy`, `show -json`, `output -json`, and `refresh-only` via `os/exec`. Writes `portal.auto.tfvars.json` (0600) before each apply. Default timeout: 20 minutes.
- `internal/proxmox/client.go`: Thin Proxmox REST API client for lightweight operations (start/stop/status) that don't warrant a Terraform apply. Uses `PVEAPIToken=` auth header, skips TLS verification (matches Terraform provider config).
- `config.json`: Defines allowed templates (name → VMID) and instance types. All user inputs validated against this allowlist.
- `protected.json`: Runtime-managed list of protected VM names. Created automatically; do not edit manually while the portal is running.
- `web/templates/`: `index.html` (instances table + launch form), `result.html` (Terraform logs).

**`infra/`** — Terraform configuration for Proxmox
- `main.tf`: Defines EC2-style flavor locals (general/compute/memory × nano–xlarge) and a `proxmox_virtual_environment_vm` resource with `for_each`. Uses `stop_on_destroy = true` to force-stop VMs before deletion.
- `variables.tf`: All configurable inputs (VMIDs, template, node, bridge, cloud-init user, SSH key, datastore, clone mode, PVE credentials).
- `outputs.tf`: Returns provisioned instances filtered to `10.*` IPs.
- `portal.auto.tfvars.json`: Auto-generated at runtime — do not edit manually.

## Key Behaviors

- The portal validates templates and instance types against `config.json` allowlists before doing anything.
- VMID range: 100–999999; count: 1–50.
- Before each apply, existing VMs are read via `terraform show -json` (not `terraform output -json` — outputs are cached and go stale after `terraform state rm`). New VMs are checked for name and VMID conflicts, then merged into the var file so prior VMs are never destroyed by a new launch.
- A `terraform apply -refresh-only` pass runs after provisioning to discover DHCP-assigned IPs.
- Terminate uses `terraform apply -destroy -target=...` for single-VM deletion. Requires typing the VM name; protected VMs additionally require typing `"destroy protected instance"`.
- Start/Stop use direct Proxmox API calls (not Terraform) for speed. Stop sends an ACPI shutdown signal (graceful). VM power state is fetched in parallel for all instances on every page load.
- Terraform state lives in `infra/terraform.tfstate` (not remote).

## Tests

```bash
cd portal && go test ./...
```

Tests cover `extractVMsFromShow` (parses `terraform show -json` into a name→vmid map), `parseInstancesFromShow` (full instance list including IP extraction), and `mergeVMs` (conflict detection and merge logic).
