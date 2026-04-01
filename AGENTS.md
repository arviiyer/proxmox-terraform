# AGENTS.md
## Scope
This repo contains the forge application only.

Components:
- `portal/`: forge-style Proxmox VM provisioner.
- `infra/`: Terraform used by `portal/`.

Repo facts:
- No root `go.mod`; run Go commands from `portal/`.
- The Go module targets Go `1.25`.
- No Makefile exists.
- No Cursor rules were found in `.cursor/rules/` or `.cursorrules`.
- No Copilot instructions were found in `.github/copilot-instructions.md`.

## Build
```bash
cd portal && go build ./...
cd portal && go build -o forge .
docker build -t forge -f Dockerfile .
```

## Run
```bash
cd portal && go run main.go
```
Required env vars: `PVE_ENDPOINT`, `PVE_API_TOKEN`, `SSH_PUBLIC_KEY`
Optional env var: `SSH_NODE_KEY_FILE`

## Test
Run all tests:
```bash
cd portal && go test ./...
```
Run a single test by name:
```bash
cd portal && go test -run TestParseLaunchForm ./...
```
Run one exact test with cache disabled:
```bash
cd portal && go test -count=1 -run '^TestMergeVMs$' ./...
```
Run only the current package tests:
```bash
cd portal && go test
```
Useful existing test names: `TestParseLaunchForm`, `TestMergeVMs`, `TestExtractVMsFromShow`, `TestParseInstancesFromShow`, `TestLoadSaveProtected`, `TestStripANSI`

## Lint And Format
There is no configured repo linter such as `golangci-lint`.
The effective quality bar is `gofmt`, `terraform fmt`, `go test`, and `go build`.
```bash
cd portal && gofmt -w .
cd portal && gofmt -l .
terraform fmt -recursive infra
terraform fmt -check -recursive infra
cd portal && gofmt -l . && go test ./... && go build ./...
terraform fmt -check -recursive infra
```

## Code Style
Follow existing repository patterns over generic preferences.

Imports:
- Group stdlib imports first, then a blank line, then local module imports.
- Keep aliases short and meaningful.
- Preserve existing aliases like `pve` and `tf`.
- Do not add aliases unless they improve clarity.

Formatting:
- Always use `gofmt` for Go and `terraform fmt` for Terraform.
- Keep comments sparse and useful.
- Add comments only for non-obvious behavior or tricky control flow.

Types:
- Prefer concrete structs for config, forms, jobs, template data, and JSON payloads.
- Use `map[string]any` only where dynamic Terraform JSON is already being consumed.
- Keep explicit `json` tags on serialized structs.
- Do not simplify API field types without verifying live behavior first.

Naming:
- Export names only when cross-package reuse requires it.
- Keep package-local helpers lowercase.
- Prefer explicit nouns like `Config`, `Instance`, `Job`, `Runner`, `Client`.
- Use short receiver names like `c`, `r`, and `j`.
- Promote repeated magic strings and timeouts into constants.

Error handling:
- Return early on invalid methods, bad forms, and validation failures.
- Use `http.Error` with specific status codes in handlers.
- Use `log.Fatal` or `log.Fatalf` for startup-time required config/env failures.
- Wrap returned errors with context using `fmt.Errorf("...: %w", err)`.
- Keep user-facing error messages short and operational.
- Log enough context around async jobs and Proxmox/Terraform operations to debug failures.

Concurrency:
- Protect shared mutable state with `sync.Mutex`.
- Use `sync.WaitGroup` for simple fan-out work like parallel VM status checks.
- Keep critical sections small.
- Prefer best-effort parallel reads when partial failure is acceptable.

HTTP and templates:
- Stay with the standard library: `net/http` and `html/template`.
- Register routes directly with `http.HandleFunc`.
- Keep handlers linear: validate, load state, act, render or redirect.
- Prefer server-rendered HTML over adding client-side complexity.

Tests:
- Prefer table-driven tests for validation and parsing logic.
- Use `t.Run(...)` for subtests.
- Use `t.TempDir()` for filesystem tests.
- Keep tests focused on parsing, validation, merge logic, and regression-prone helpers.

Terraform:
- Keep Terraform explicit and minimal.
- Preserve provider pinning unless intentionally upgrading.
- Keep `config.json` defaults and Terraform assumptions aligned.
- Do not manually edit generated `portal.auto.tfvars.json` files.

## Repo-Specific Gotchas
- Prefer `terraform show -json` over `terraform output -json` for current resource truth.
- Preserve `stop_on_destroy = true` for clean VM deletion behavior.
- The portal merges existing VMs into the var file before apply; avoid changes that would make a new launch destroy existing VMs.
- User-data snippet upload is optional and relies on the Proxmox API plus `SSH_NODE_KEY_FILE` for provider SSH access.

## Operational Rules
- Read this repo’s `CLAUDE.md` before substantial work.
- For operational changes, also read the parent repo `CLAUDE.md`, architecture file, and git workflow guide.
- If you edit live server config outside this repo, copy it back to the matching `~/repos/<host>` checkout and commit there.
- If you change OPNsense rules, export the config XML and commit it under `homelab-projects/homelab-network/config/`.

## Agent Guidance
- Make the smallest correct change.
- Prefer existing patterns over new abstractions.
- Avoid adding dependencies; Go code here is intentionally stdlib-first.
- Before finishing, verify formatting, tests, and build in `portal/` and formatting in `infra/`.
