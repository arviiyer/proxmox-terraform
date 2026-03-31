package terraform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Runner struct {
	Dir string // terraform working dir
}

func (r Runner) Init(ctx context.Context) error {
	return r.run(ctx, "init", "-input=false")
}

// Apply runs terraform apply with the provided var-file JSON.
// It returns stdout+stderr combined to show logs in the UI if needed.
func (r Runner) Apply(ctx context.Context, varFile string) (string, error) {
	out, err := r.runCombined(ctx, "apply", "-auto-approve", "-input=false", "-var-file", varFile)
	return out, err
}

// RefreshOnly updates outputs/state without changing infrastructure.
func (r Runner) RefreshOnly(ctx context.Context, varFile string) (string, error) {
	out, err := r.runCombined(ctx, "apply", "-refresh-only", "-auto-approve", "-input=false", "-var-file", varFile)
	return out, err
}

// Destroy removes a single resource by Terraform address.
func (r Runner) Destroy(ctx context.Context, target, varFile string) (string, error) {
	return r.runCombined(ctx, "apply", "-destroy", "-auto-approve", "-input=false",
		"-target", target, "-var-file", varFile)
}

// StateRm removes a resource from Terraform state without modifying live infra.
func (r Runner) StateRm(ctx context.Context, target string) (string, error) {
	return r.runCombined(ctx, "state", "rm", target)
}

// OutputJSON returns `terraform output -json` parsed.
func (r Runner) OutputJSON(ctx context.Context) (map[string]any, error) {
	out, err := r.runCapture(ctx, "output", "-json")
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse terraform output json: %w", err)
	}
	return m, nil
}

// ShowJSON returns `terraform show -json` parsed. Unlike OutputJSON, this
// reads actual resource state rather than cached output values, so it remains
// accurate after terraform state rm operations.
func (r Runner) ShowJSON(ctx context.Context) (map[string]any, error) {
	out, err := r.runCapture(ctx, "show", "-json")
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse terraform show json: %w", err)
	}
	return m, nil
}

func (r Runner) run(ctx context.Context, args ...string) error {
	_, err := r.runCombined(ctx, args...)
	return err
}

func (r Runner) runCapture(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "terraform", args...)
	cmd.Dir = r.Dir
	cmd.Env = os.Environ()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("terraform %v failed: %w\n%s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (r Runner) runCombined(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "terraform", args...)
	cmd.Dir = r.Dir
	cmd.Env = os.Environ()

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("terraform %v failed: %w", args, err)
	}
	return buf.String(), nil
}

func WriteVarFileJSON(dir string, payload any) (string, error) {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "portal.auto.tfvars.json")
	// 0600 because this file may contain ssh keys/usernames (not super secret, but keep tight)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func DefaultTimeoutCtx() (context.Context, context.CancelFunc) {
	// cloning can take a bit depending on storage; 20m is safe for homelab
	return context.WithTimeout(context.Background(), 20*time.Minute)
}
