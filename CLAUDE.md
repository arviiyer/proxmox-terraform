# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Project Does

Two Go web applications in the same repository:

1. **forge** (`portal/`) — EC2-like interface for provisioning homelab VMs on Proxmox using Terraform. Accessible at `forge.arviiyer.dev` via Tailscale only. Users fill out a web form; the portal generates a Terraform vars file and runs `terraform apply` against the `infra/` directory.

2. **sandbox** (`sandbox/`) — Malware analysis and reverse engineering VM provisioner. Accessible at `sandbox.arviiyer.dev` via Cloudflare Tunnel + Cloudflare Access (email OTP). Built for SOC/malware analysis work from devices that cannot run Tailscale (e.g. corporate laptop with AnyConnect VPN). Shares `internal/proxmox` and `internal/terraform` packages with forge but has its own Terraform directory (`sandbox-infra/`) and its own Proxmox API token scoped to skyrim only.

## Running and Building

```bash
# Run the portal (serves on :8088)
cd portal && go run main.go

# Build binary
cd portal && go build -o portal
```

These env vars are required — the server fatals at startup if any are missing:
```bash
PVE_ENDPOINT="https://your-proxmox-host"   # no :8006 if behind a reverse proxy
PVE_API_TOKEN="user@pam!tokenid=secret"
SSH_PUBLIC_KEY="ssh-ed25519 ..."           # injected into VMs at cloud-init
```

Optional:
```bash
SSH_NODE_KEY_FILE="/home/user/.ssh/id_ed25519"  # defaults to ~/.ssh/id_ed25519
```
The private key at `SSH_NODE_KEY_FILE` is used by the bpg/proxmox Terraform provider to SSH into Proxmox nodes as `root` when writing snippet files (user data). Only needed when launching VMs with user data.

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
- Start/Stop use direct Proxmox API calls (not Terraform) for speed. Stop sends a graceful ACPI shutdown with `forceStop=1` fallback. VM power state is fetched in parallel for all instances on every page load.
- User data (cloud-init) is optional. If provided, the portal uploads it as a snippet to `proxmox-nas` via the Proxmox API before calling Terraform, then passes the file ID (`proxmox-nas:snippets/<filename>`) as `user_data_file_id`. Snippet files are named `portal-<nameprefix>-<vmcount>.yaml` and are not auto-deleted on terminate.
- Terraform state lives in `infra/terraform.tfstate` (not remote).

## Tests

```bash
cd portal && go test ./...
```

Tests cover `extractVMsFromShow` (parses `terraform show -json` into a name→vmid map), `parseInstancesFromShow` (full instance list including IP extraction), and `mergeVMs` (conflict detection and merge logic).

---

## Sandbox Project

### Purpose

A purpose-built malware analysis and reverse engineering VM provisioner for SOC/blue team work. Replaces reliance on short-lived public sandboxes (any.run etc.) with a self-hosted environment the analyst controls. Accessible from a corporate laptop without Tailscale.

### Architecture

```
sandbox/
  main.go                 # HTTP server — routes, ephemeral watcher goroutine
  config.json             # allowed templates (windows-analysis, remnux) + VM sizes
  ephemeral.json          # runtime state — which VMs auto-destroy on shutdown (gitignored)
  web/templates/
    index.html            # dashboard: running VMs + launch form
    result.html           # Terraform log output

sandbox-infra/            # separate Terraform directory — own state, own defaults
  main.tf
  variables.tf
  outputs.tf
  portal.auto.tfvars.json # runtime-generated, gitignored
```

Shares `portal/internal/proxmox` and `portal/internal/terraform` packages directly. The terraform `Runner` takes a `Dir` field so it works unchanged against `sandbox-infra/`.

### VM Types

- **Persistent** — RE workstation (Windows or Linux). Stays up indefinitely. For tool installation, long-running analysis, reverse engineering.
- **Ephemeral** — Sandbox VM for malware detonation. Auto-destroyed when powered off. A background goroutine in `main.go` polls power state every 30s; when an ephemeral VM transitions to `stopped`, it triggers `terraform destroy` automatically and removes it from `ephemeral.json`.

### Key Differences from Forge

- No protection toggle (not needed)
- No user data / cloud-init upload
- Ephemeral auto-destroy goroutine (not in forge)
- Snapshot and revert support (new Proxmox API methods in `internal/proxmox/client.go`)
- In-browser console via WebSocket proxy (sandbox app proxies noVNC WebSocket to Proxmox node — Proxmox is never directly exposed to the internet)
- All sandbox VMs run on **skyrim node only** (`10.0.0.12`) on isolated bridge `vmbr1` (no LAN uplink)
- Uses a **dedicated limited PVE API token** scoped to skyrim node only — not the same token as forge
- `terraform.Runner{Dir: "../sandbox-infra"}` (not `../infra`)

### Routes

| Route | Purpose |
|---|---|
| `GET /` | Dashboard — list VMs + launch form |
| `POST /launch` | Provision VM (persistent or ephemeral) |
| `POST /destroy` | Manual destroy |
| `POST /start` | Start stopped VM |
| `POST /stop` | Stop running VM |
| `POST /snapshot` | Take named snapshot |
| `POST /revert` | Revert to snapshot |
| `GET /console/{name}` | Serve noVNC page proxied through sandbox app |
| `GET /ws/{name}` | WebSocket endpoint — proxies VNC traffic to Proxmox node |

### Access Model

- External: `sandbox.arviiyer.dev` → Cloudflare Tunnel (cloudflared on morrowind) → `10.0.0.83:8089`
- Cloudflare Access policy: email OTP, work email allowlisted, 8-hour session
- LAN/Tailscale: Caddy vhost on morrowind `sandbox.arviiyer.dev` → `10.0.0.83:8089` with `@allow` block
- Proxmox web UI (`pve.arviiyer.dev`) stays Tailscale-only — never exposed via tunnel

### Deployment

- Docker container on srv-apps, port `8089`, at `/srv/sandbox/`
- Same env vars as forge (`PVE_ENDPOINT`, `PVE_API_TOKEN`, `SSH_PUBLIC_KEY`) but `PVE_API_TOKEN` is a different token scoped to skyrim
- Compose file committed to `homelab/srv-apps` Forgejo repo

### Security Mitigations

| Mitigation | Detail |
|---|---|
| Dedicated PVE API token | Scoped to skyrim node only, VM operations only — not the forge token |
| Sandbox VMs on skyrim only | Isolates VM escape blast radius to the least critical node |
| vmbr1 isolated bridge | No physical NIC attached, no uplink — air-gapped from LAN |
| OPNsense: block sandbox range → LAN | `10.0.2.0/24` → `10.0.0.0/24` blocked as backstop against IP forwarding misconfiguration |
| OPNsense: block skyrim → critical VMs | Limits post-escape lateral movement from skyrim to homelab services |
| OPNsense: sandbox egress allow-list | If internet enabled for detonation: allow WAN, block all RFC1918 |
| No IP forwarding on skyrim | `net.ipv4.ip_forward = 0` verified after setup |
| WebSocket proxy (no Proxmox exposure) | Console access proxied through sandbox app — Proxmox never behind CF Tunnel |
| CF Access session limit | 8-hour sessions, Purpose Justification prompt enabled |
| Input allowlisting | Templates and VM sizes validated against `config.json` before any Terraform operation |

### Implementation Phases

- **Phase 0** — Proxmox: create `vmbr1` on skyrim, verify/register analysis templates in config
- **Phase 1** — Cloudflare: cloudflared tunnel on morrowind, CF Access policy, Caddy vhost
- **Phase 2** — `sandbox-infra/`: Terraform directory with isolated bridge defaults, own state
- **Phase 3** — Extend `internal/proxmox/client.go`: `ListSnapshots`, `CreateSnapshot`, `RevertSnapshot`
- **Phase 4** — `sandbox/` Go binary: main.go, config.json, templates, ephemeral watcher, WS proxy
- **Phase 5** — Deployment: Docker compose on srv-apps, Forgejo commit
- **Phase 6** — Docs: update `tamriel-homelab-architecture.md` in homelab-projects
