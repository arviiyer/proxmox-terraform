# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Cross-Repo Context — Read These at the Start of Every Session

**Read all three of these files at the start of every session, including after context compaction.** This context is frequently lost during compaction and must be re-loaded manually.

- `~/codebase/homelab-projects/CLAUDE.md` — cluster topology, critical constraints (SECRET_KEY, NFS gotchas, Caddy reload), deployment pattern, git workflow overview
- `~/codebase/homelab-projects/tamriel-homelab-architecture.md` — authoritative IP/service/domain reference; update it whenever a VM, service, or Caddy vhost changes
- `~/codebase/homelab-projects/self-hosted-git/git-workflow.md` — **required git workflow**: every config change on a server must be copied to `~/repos/<host>` and committed to the matching Forgejo repo (`homelab/<host>`) after the live edit

## OPNsense API Access

OPNsense (10.0.0.1) API key is at `~/codebase/homelab-projects/proxmox-terraform/oblivian.internal_root_apikey.txt` (gitignored — never commit).
Usage: `curl -sk -u "$KEY:$SECRET" https://10.0.0.1/api/...`
Apply rules after changes: `POST /api/firewall/filter/apply`
Export config: `GET /api/core/backup/download/this`
After any OPNsense change: export config XML → commit to `homelab-projects/homelab-network/config/config-YYYY-MM-DD.xml`.

## Sandbox — Current Deployment Status

- **Container:** running on srv-apps port 8089, `restart: unless-stopped`, compose at `/srv/sandbox/`
- **Build workflow:** Docker build context is `/home/arvind/repos/proxmox-terraform` on srv-apps (git clone of Forgejo). Always `git push forgejo master` first, then `git pull` on srv-apps, then `docker compose build --no-cache && docker compose up -d`. Changes to `/home/arvind/codebase/homelab-projects/proxmox-terraform` on local machine do NOT auto-sync — must push+pull.
- **PVE token:** `root@pam!sandbox` on summerset — ACLs: `/nodes/summerset` PVEVMAdmin, `/storage/local-lvm` PVEDatastoreAdmin, `/vms` PVEVMAdmin (needed for VM.Allocate on dest VMID), `/sdn/zones/localnetwork` PVESDNUser (SDN.Use checked on template NIC bridge even when clone overrides it), all privsep=1
- **Phases complete:** 0–6 plus UI polish — fully deployed, externally accessible, console working ✅
- **Access:** `sandbox.arviiyer.dev` via CF Tunnel (privacy-lab, `a531fa13-40c3-45b2-a251-ee4e624d2cfb`) + CF Access (One-time PIN). Allowlist: `@toh.ca` domain + `rbarvind04@gmail.com`. toh.ca OTP emails blocked by their mail gateway — use Gmail OTP from corporate laptop.
- **Security mitigations in place:** vmbr1 isolated bridge, iptables DROP on summerset (persisted via `sandbox-iptables.service`), dnsmasq DHCP-only on vmbr1 (`/etc/dnsmasq.d/vmbr1-sandbox.conf`, range 10.0.2.100–200), OPNsense block rules (10.0.2.0/24→LAN; summerset→Authentik; summerset→PBS), dedicated scoped PVE token
- **Internet-mode live status (2026-03-30):** `vmbr2` now exists on summerset via `nic0.60`, host firewall backstops now block `vmbr2` from `vmbr0`/`vmbr1`, OPNsense now has `SANDBOX_PUBLIC` (`opt5`, `10.60.0.1/24`) with public-only deny-first rules plus explicit firewall-interface deny rules (`712-717`), and the Unifi switch now tags VLAN 60 on the OPNsense uplink (port 16) and summerset port (port 2). A disposable guest on `vmbr2` successfully received `10.60.0.101/24`, failed to ping `10.60.0.1` and `10.0.0.1`, reached public internet (`1.1.1.1`), and could not ping `10.0.0.11`, `10.0.0.83`, or `100.64.0.1`.
- **FakeNet live status (2026-03-30):** responder VM `8050` (`sandbox-fakenet`) now runs on `vmbr1` at `10.0.2.53`. INetSim serves the fake application protocols on that IP, and a local `fakenet-dns.service` answers wildcard DNS A queries to `10.0.2.53`. App-backed FakeNet launches from `sandbox.arviiyer.dev` are now working end-to-end: a disposable UI-launched guest was validated with `/etc/resolv.conf` set to `nameserver 10.0.2.53`, `example.com` resolving to `10.0.2.53`, `curl http://example.com` returning `HTTP/1.1 200 OK` from `INetSim HTTP Server`, and `ping 1.1.1.1` failing. The previous guest-agent timeout/orchestration bug is fixed.
- **Submit URL status (2026-03-31):** Linux `Submit URL` is working. Windows `Submit URL` now stages the submitted URL file, creates the public desktop `.url` launcher, and launches Edge in the guest, but the app can still leave the job page stuck at `Submitting URL…` even after guest-side success. Logs show the remaining issue is backend job-completion/orchestration, not Windows guest staging itself.
- **Risk acceptance:** residual risks reviewed and accepted with due diligence (2026-03-28)

## Sandbox — Known Gotchas (hard-won fixes, do not regress)

- **VNCProxy port type:** Proxmox returns `port` as a JSON **string** when called with `websocket=1`. `VNCProxyResult.Port` is `string`, not `int`. Do not change it back to `int`.
- **Sandbox clone CPU model:** all sandbox templates use CPU type `x86-64-v2-AES`. In Terraform, `cpu { cores = ... }` without an explicit `type` makes the provider rewrite clones to `qemu64`, which broke Windows sandbox boots. Keep `type = "x86-64-v2-AES"` in `sandbox-infra/main.tf` unless the templates are rebuilt with a different CPU model.
- **Live sandbox-infra mount:** the sandbox container runs against the bind-mounted `/srv/sandbox/sandbox-infra` directory on srv-apps, not the repo checkout directly. The image entrypoint now syncs bundled `*.tf` files into `/sandbox-infra` at container start so deploys cannot silently keep stale Terraform config from a previous rollout.
- **VNC ticket encoding:** Pass ticket to console.html template as `template.JS(json.Marshal(vnc.Ticket))` — NOT `vnc.Ticket` with `| js` in the template. Using `| js` causes double-escaping (html/template applies JS escaping on top of `| js`), turning `=` into the literal string `\u003D`, which Proxmox rejects with 401.
- **WebSocket proxy path:** handleWS connects to `{pveHost}:8006` (NOT the raw vnc.Port number) and upgrades to `/api2/json/nodes/{node}/qemu/{vmid}/vncwebsocket?port={port}&vncticket={ticket}`. Ticket and port are passed via WS URL query params from console.html to avoid a second VNCProxy call (which generates a different ticket).
- **Hijack flush:** After `hj.Hijack()`, call `clientBuf.Flush()` before starting the bidirectional relay, or the 101 response never reaches the client.
- **PVE token ACLs needed for clone:** `/vms` (VM.Allocate on destination VMID) + `/sdn/zones/localnetwork` (SDN.Use on template's NIC bridge) — both required in addition to `/nodes/summerset` and `/storage/local-lvm`.
- **Terraform state + build context:** sandbox-infra state lives at `/srv/sandbox/sandbox-infra/` (volume mount). If state gets out of sync with Proxmox, check tfstate manually. Never run terraform manually from outside the container against the same state file.
- **Job pattern:** both launch and destroy use async jobs (redirect to `/?job=ID`). handleIndex reads `?job=` param, shows banner + placeholder launching/terminating rows. `/job/{id}` page shows Terraform output with 3s auto-refresh while running.
- **Stale Terraform outputs:** targeted destroys can leave `terraform output -json` values behind even after the VM and resource are gone. For sandbox instance listing and merge logic, trust `terraform show -json` resources, not cached outputs.
- **FakeNet orchestration fix:** post-launch FakeNet now waits for VM running state, waits for guest-agent-reported non-loopback IPv4 networking, uses the SSH key path (not key contents) for backend guest commands, suppresses SSH host-key warning noise so JSON parsing stays clean, and verifies that the guest resolver actually ends up on `10.0.2.53` before marking the job done.
- **FakeNet responder recovery:** VM `8050` is not managed by sandbox Terraform state. If it disappears from Proxmox, rebuild it from template `8010` with [`infra/rebuild-sandbox-fakenet.sh`](/home/arvind/codebase/homelab-projects/proxmox-terraform/infra/rebuild-sandbox-fakenet.sh) and then revalidate an app-backed FakeNet launch.
- **Windows URL handoff reality:** Edge first-run/sync onboarding can intercept a freshly launched Windows sandbox and hide the submitted URL behind setup screens. Even when backend staging succeeds, the analyst may first see Edge welcome/sign-in UI unless the Windows template is hardened to suppress first-run flows.
- **Windows URL retry-loop symptom:** if the backend does not accept the first successful Windows handoff, repeated staging retries can spawn a new submitted-URL tab every few seconds. Treat verified presence of `C:\Sandbox\Incoming\submitted-url.txt` and `C:\Users\Public\Desktop\Open Submitted URL.url` as terminal success for Windows URL staging.
- **Disposable validation rule:** do not run more than one disposable validation VM at a time on summerset. Destroy the test VM immediately after each validation step before starting another one.

## Sandbox — Network Mode Status

Treat sandbox networking as a segmentation project first and an app feature second. As of 2026-03-30, the first full implementation is live:

- Sandbox app/UI models explicit per-VM network modes (`Offline`, `FakeNet`, `Internet`) and records them in `sandbox-infra/metadata.json`.
- Terraform input is per-VM (`template_vmid`, `instance_type`, `bridge`) so launching a VM in one mode does not rewrite existing VMs onto the same bridge/size.
- `Internet` is sandbox-only and ephemeral-only in the app.
- Live Proxmox bridge work, OPNsense public-only policy, host firewall backstops, and Unifi VLAN carriage are done for `Internet`.
- FakeNet service delivery is live through the dedicated responder VM on `vmbr1`.
- App-backed FakeNet launch automation is now complete and validated from the sandbox UI.
- `Submit File` and `Submit URL` remain unimplemented; the current product is a working sandbox launcher/provisioner, not yet an intake-driven analysis workflow.

- **Target modes:** `Offline`, `FakeNet`, and `Internet (public-only)`.
- **Offline:** preserve current behavior on `vmbr1`. This remains the default and must stay truly isolated.
- **FakeNet:** simulate common network services for detonation without exposing the guest to the homelab or the public internet.
- **Internet (public-only):** allow outbound public internet access for URL analysis / realistic C2 detonation, but never provide a path into the homelab, RFC1918 space, or Tailscale ranges.

### Required design constraints

- Keep `vmbr1` as the offline sandbox network. Do **not** reuse it for internet-enabled analysis.
- Internet-enabled detonation should use a separate dedicated network path/bridge/VLAN with routing enforced by OPNsense.
- OPNsense policy for internet-enabled analysis must be WAN-only NAT plus explicit denies for:
  - RFC1918 ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`)
  - Tailscale CGNAT range (`100.64.0.0/10`)
  - Homelab control-plane and service IPs (Proxmox nodes, OPNsense admin, PBS, Authentik, Forgejo, NAS, observability, and other critical internal services)
- Keep host-level firewall backstops on `summerset` so analysis guests cannot reach sensitive services even if routing drifts later.
- Internet-enabled runs should be **ephemeral by default**. Persistent RE workspaces should remain offline unless time-limited internet access is explicitly designed later.
- The sandbox UI should expose network mode explicitly per run, with `Offline` as the default.
- Record the selected network mode in app/job metadata so analysis runs are auditable.

### Validation baseline

1. `Offline`: stays on `vmbr1` with no public route.
2. `FakeNet`: stays on `vmbr1`, rewrites guest DNS to `10.0.2.53`, resolves arbitrary names to the responder, and serves simulated services from INetSim without a real internet path. This is validated from the live sandbox UI.
3. `Internet`: uses `vmbr2` / VLAN 60, receives `10.60.0.0/24`, reaches public internet, and is denied RFC1918, Tailscale, homelab service IPs, and firewall self IPs.

### Next planned phase

- `Submit URL`: accept a URL, launch an ephemeral guest in the selected mode, stage the URL inside the guest, and hand the analyst off to console.
- `Submit File`: accept a sample upload, launch an ephemeral guest in the selected mode, stage the file inside the guest, and hand the analyst off to console.
- Both intake modes should stay sandbox-only, default to ephemeral detonation, and avoid mixing uploads or staged artifacts into persistent workspaces.
- Before calling Windows `Submit URL` complete, also harden the Windows sandbox template so Edge first-run/sync/sign-in prompts do not intercept the analyst workflow.

### Cross-repo reminder

- If OPNsense rules change: export the config XML and commit it to `homelab-projects/homelab-network/config/`.
- If srv-apps runtime or compose config changes: copy them back to the matching `homelab/srv-apps` repo per `git-workflow.md`.

## What This Project Does

Two Go web applications in the same repository:

1. **forge** (`portal/`) — EC2-like interface for provisioning homelab VMs on Proxmox using Terraform. Accessible at `forge.arviiyer.dev` via Tailscale only. Users fill out a web form; the portal generates a Terraform vars file and runs `terraform apply` against the `infra/` directory.

2. **sandbox** (`sandbox/`) — Malware analysis and reverse engineering VM provisioner. Accessible at `sandbox.arviiyer.dev` via Cloudflare Tunnel + Cloudflare Access (email OTP). Built for SOC/malware analysis work from devices that cannot run Tailscale (e.g. corporate laptop with AnyConnect VPN). Shares `portal/proxmox` and `portal/terraform` packages with forge but has its own Terraform directory (`sandbox-infra/`) and its own Proxmox API token scoped to summerset only.

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
- `terraform/runner.go`: Wraps `terraform init`, `apply`, `apply -destroy`, `show -json`, `output -json`, and `refresh-only` via `os/exec`. Writes `portal.auto.tfvars.json` (0600) before each apply. Default timeout: 20 minutes.
- `proxmox/client.go`: Thin Proxmox REST API client for lightweight operations (start/stop/status) that don't warrant a Terraform apply. Uses `PVEAPIToken=` auth header, skips TLS verification (matches Terraform provider config).
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
    console.html          # noVNC page (CDN) with embedded VNC ticket

sandbox-infra/            # separate Terraform directory — own state, own defaults
  main.tf
  variables.tf
  outputs.tf
  portal.auto.tfvars.json # runtime-generated, gitignored
```

Shares `portal/proxmox` and `portal/terraform` packages directly (moved out of internal/ so sandbox can import them). The terraform `Runner` takes a `Dir` field so it works unchanged against `sandbox-infra/`.

### VM Types

- **Persistent** — RE workstation (Windows or Linux). Stays up indefinitely. For tool installation, long-running analysis, reverse engineering.
- **Ephemeral** — Sandbox VM for malware detonation. Auto-destroyed when powered off. A background goroutine in `main.go` polls power state every 30s; when an ephemeral VM transitions to `stopped`, it triggers `terraform destroy` automatically and removes it from `ephemeral.json`.

### Key Differences from Forge

- No protection toggle (not needed)
- No user data / cloud-init upload
- Ephemeral auto-destroy goroutine (not in forge)
- Snapshot and revert support (new Proxmox API methods in `portal/proxmox/client.go`)
- In-browser console via WebSocket proxy (sandbox app proxies noVNC WebSocket to Proxmox node — Proxmox is never directly exposed to the internet)
- All sandbox VMs run on **summerset node only** (`10.0.0.11`) on isolated bridge `vmbr1` (no LAN uplink, 10.0.2.0/24)
- Uses a **dedicated limited PVE API token** scoped to summerset node only — not the same token as forge
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
- Same env vars as forge (`PVE_ENDPOINT`, `PVE_API_TOKEN`, `SSH_PUBLIC_KEY`) but `PVE_API_TOKEN` is a different token scoped to summerset
- Compose file committed to `homelab/srv-apps` Forgejo repo

### Security Mitigations

| Mitigation | Detail |
|---|---|
| Dedicated PVE API token | Scoped to summerset node only, VM operations only — not the forge token |
| Sandbox VMs on summerset only | Isolates VM escape blast radius; skyrim ruled out (iGPU VRAM leaves only 2GB free) |
| vmbr1 isolated bridge | On summerset: no physical NIC attached, no uplink — air-gapped from LAN (10.0.2.0/24) |
| OPNsense: block sandbox range → LAN | `10.0.2.0/24` → `10.0.0.0/24` blocked as backstop against IP forwarding misconfiguration |
| OPNsense: block summerset → critical VMs | Limits post-escape lateral movement from summerset to homelab services |
| OPNsense: sandbox egress allow-list | If internet enabled for detonation: allow WAN, block all RFC1918 |
| No IP forwarding on skyrim | `net.ipv4.ip_forward = 0` verified after setup |
| WebSocket proxy (no Proxmox exposure) | Console access proxied through sandbox app — Proxmox never behind CF Tunnel |
| CF Access session limit | 8-hour sessions, Purpose Justification prompt enabled |
| Input allowlisting | Templates and VM sizes validated against `config.json` before any Terraform operation |

### Sandbox VM Templates (on summerset)

| VMID | Name | Status | Notes |
|---|---|---|---|
| 8010 | debian13-sandbox | ✅ Done | debian-13-standard, Tailscale/NodeExporter/Promtail removed |
| 8020 | remnux | ✅ Done | REMnux Ubuntu Noble 24.04 (proxmox qcow2), cloud-init removed |
| 8030 | win11-sandbox | ✅ Done | Win11 Pro, Defender/UAC/WU disabled, RDP on, VirtIO+QEMU agent |
| 8040 | win11-flare | ✅ Done | Clone of 8030, FlareVM toolkit (217 packages: x64dbg, FLOSS, dnSpy, BinDiff, etc.) |

**win11-flare setup notes (for future re-creation):**
- FlareVM install.ps1 must run as a regular user (not SYSTEM) — SYSTEM context breaks BoxStarter after Explorer restart
- Set autologon for the user account, add HKLM Run key to launch install.ps1, reboot — BoxStarter continues through reboots automatically
- `vm-packages` Chocolatey source (MyGet) is configured automatically by the script
- After install: disable autologon, remove install scripts, shutdown, `qm template <vmid>`

### Implementation Phases

- **Phase 0** — Proxmox: ✅ create `vmbr1` on summerset, ✅ all 4 VM templates registered in config
- **Phase 1** — Cloudflare: cloudflared tunnel on morrowind, CF Access policy, Caddy vhost
- **Phase 2** — ✅ `sandbox-infra/`: Terraform directory with isolated bridge defaults, own state
- **Phase 3** — ✅ Extend `portal/proxmox/client.go`: `ListSnapshots`, `CreateSnapshot`, `RevertSnapshot`
- **Phase 4** — ✅ `sandbox/` Go binary: main.go, config.json, templates, ephemeral watcher, WS proxy
- **Phase 5** — Deployment: Docker compose on srv-apps, Forgejo commit
- **Phase 6** — Docs: update `tamriel-homelab-architecture.md` in homelab-projects
