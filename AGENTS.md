# AGENTS.md

## Scope
This repository contains the `forge` application only.

Components:
- `portal/`: Go web app that provisions and manages Proxmox VMs.
- `infra/`: Terraform used by `portal/` to create and destroy VMs.

## Read First
Before substantial work, read:
- `CLAUDE.md` in this repo.
- `~/codebase/homelab-projects/CLAUDE.md`.
- `~/codebase/homelab-projects/tamriel-homelab-architecture.md`.
- `~/codebase/homelab-projects/self-hosted-git/git-workflow.md`.

## Repo Facts
- There is no root `go.mod`; run Go commands from `portal/`.
- The Go module is `github.com/arviiyer/proxmox-terraform/portal`.
- The module targets Go `1.25`.
- There is no Makefile.
- There is no configured Go linter such as `golangci-lint`.
- Terraform version requirement is `>= 1.5.0`.
- The pinned Terraform provider is `bpg/proxmox ~> 0.6`.
- Docker builds only the Go app; `infra/` is used separately at runtime.
- No Cursor rules were found in `.cursor/rules/` or `.cursorrules`.
- No Copilot instructions were found in `.github/copilot-instructions.md`.

## Commands
Build, run, test, and format from the actual repo layout:

```bash
# Go build
cd portal && go build ./...
cd portal && go build -o forge .

# Local run
cd portal && go run main.go

# Tests
cd portal && go test ./...
cd portal && go test
cd portal && go test -run TestParseLaunchForm ./...
cd portal && go test -count=1 -run '^TestMergeVMs$' ./...

# Formatting / checks
cd portal && gofmt -w .
cd portal && gofmt -l .
terraform fmt -recursive infra
terraform fmt -check -recursive infra

# Good final verification
cd portal && gofmt -l . && go test ./... && go build ./...
terraform fmt -check -recursive infra

# Container build
docker build -t forge -f Dockerfile .
```

Required env vars for local run:
- `PVE_ENDPOINT`
- `PVE_API_TOKEN`
- `SSH_PUBLIC_KEY`

Optional env var:
- `SSH_NODE_KEY_FILE`

Notes:
- If `SSH_NODE_KEY_FILE` is unset, the app falls back to `~/.ssh/id_ed25519`.
- The app listens on `:8088`.

Useful existing tests:
- `TestStripANSI`
- `TestParseLaunchForm`
- `TestLoadSaveProtected`
- `TestExtractVMsFromShow`
- `TestParseInstancesFromShow`
- `TestMergeVMs`

## Architecture Snapshot
- `portal/main.go` holds config loading, handlers, async jobs, merge logic, and template rendering.
- `portal/terraform/runner.go` wraps Terraform CLI calls with `os/exec`.
- `portal/proxmox/client.go` performs direct Proxmox API calls for start, stop, status, and protection.
- `portal/config.json` defines allowed templates, nodes, instance types, and defaults.
- `portal/protected.json` is runtime-managed state.
- `infra/main.tf` defines the VM resource and cloud-init setup.

## Code Style
Follow existing repository patterns over generic preferences.

Imports:
- Group standard library imports first.
- Put local module imports after a blank line.
- Keep aliases short and meaningful.
- Preserve the existing aliases `pve` and `tf`.
- Do not add aliases unless they improve clarity.

Formatting:
- Always use `gofmt` for Go and `terraform fmt` for Terraform.
- Keep functions and handlers linear when possible.
- Keep comments sparse and useful.
- Add comments only for non-obvious behavior, tricky control flow, or operational constraints.

Types:
- Prefer concrete structs for config, forms, jobs, template data, and JSON payloads.
- Keep explicit `json` tags on serialized structs.
- Use `map[string]any` only where dynamic Terraform JSON is already being consumed.
- Do not simplify Terraform JSON parsing types without verifying the real shape first.
- Preserve file modes and sensitive-field handling where the current code is already careful.

Naming:
- Export names only when cross-package use requires it.
- Keep package-local helpers lowercase.
- Prefer explicit nouns like `Config`, `Instance`, `Job`, `Runner`, and `Client`.
- Use short receiver names like `c`, `r`, and `j`.
- Promote repeated phrases or timeouts into constants when reuse is real.

Error handling:
- Return early on invalid methods, form parsing failures, and validation errors.
- In handlers, use `http.Error` with specific status codes.
- For startup-time required config or env failures, use `log.Fatal` or `log.Fatalf`.
- Wrap returned errors with context using `fmt.Errorf("...: %w", err)`.
- Keep user-facing error messages short and operational.
- Log enough Terraform and async-job context to debug failures.

HTTP and templates:
- Stay with the standard library: `net/http` and `html/template`.
- Register routes directly with `http.HandleFunc`.
- Keep handlers in the pattern: validate, load state, act, render or redirect.
- Prefer server-rendered HTML over adding client-side complexity.

Concurrency:
- Protect shared mutable state with `sync.Mutex`.
- Keep critical sections small.
- Use `sync.WaitGroup` for simple fan-out work such as VM status checks.
- Best-effort parallel reads are acceptable when partial failure should not break the page.
- Do not allow concurrent Terraform apply flows against the same local state.

Terraform:
- Keep Terraform explicit and minimal.
- Preserve provider version pinning unless intentionally upgrading.
- Preserve `stop_on_destroy = true`.
- Keep `config.json` defaults aligned with Terraform assumptions.
- Do not manually edit generated `portal.auto.tfvars.json` files.

Tests:
- Prefer table-driven tests for parsing and validation logic.
- Use `t.Run(...)` for subtests.
- Use `t.TempDir()` for filesystem tests.
- Keep tests focused on parsing, validation, merge logic, state extraction, and regression-prone helpers.

## Repo-Specific Gotchas
- Prefer `terraform show -json` over `terraform output -json` for current resource truth.
- Do not break the merge-before-apply behavior; it prevents unrelated VM destruction.
- A `terraform apply -refresh-only` pass runs after provisioning to pick up DHCP IPs.
- Single-VM destroy uses targeted `terraform apply -destroy -target=...`.
- Protected VMs require the literal phrase `destroy protected instance`.
- Start and stop use the direct Proxmox API rather than Terraform.
- VM status fetch on the index page is parallel and best-effort.
- The app assumes Proxmox API TLS verification is skipped, matching Terraform config.
- Terraform state is local in `infra/terraform.tfstate`.

## Files To Treat Carefully
- `portal/config.json`: checked-in runtime config; keep values aligned with Terraform assumptions.
- `portal/protected.json`: runtime-managed; avoid hand-editing while the app is running.
- `infra/portal.auto.tfvars.json`: generated by the app; do not hand-maintain.
- `oblivian.internal_root_apikey.txt`: secret, gitignored, must never be committed.
- `.env` files may exist locally; do not commit real secrets.

## Agent Guidance
- Make the smallest correct change.
- Prefer existing patterns over new abstractions.
- Avoid adding dependencies; the Go code here is intentionally stdlib-first.
- If you change launch or destroy behavior, verify both handler logic and Terraform runner behavior.
- If you touch Terraform inputs or defaults, verify `portal/config.json` and `infra/*.tf` still agree.
- Before finishing, verify Go formatting, Go tests, Go build, and Terraform formatting.
