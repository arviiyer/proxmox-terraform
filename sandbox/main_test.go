package main

import (
	"strings"
	"testing"
)

func TestValidatedNetworkModeDefaultsToOffline(t *testing.T) {
	cfg = Config{
		NetworkModes: map[string]NetworkModeConfig{
			"offline":  {Label: "Offline", Bridge: "vmbr1"},
			"internet": {Label: "Internet (public-only)", Bridge: "vmbr2", RequireEphemeral: true, SandboxOnly: true},
		},
	}
	cfg.Defaults.NetworkMode = "offline"

	spec, label, err := validatedNetworkMode(LaunchForm{}, "sandbox")
	if err != nil {
		t.Fatalf("validatedNetworkMode returned error: %v", err)
	}
	if label != "Offline" {
		t.Fatalf("label = %q, want %q", label, "Offline")
	}
	if spec.Bridge != "vmbr1" {
		t.Fatalf("bridge = %q, want %q", spec.Bridge, "vmbr1")
	}
}

func TestValidatedNetworkModeRejectsPersistentInternet(t *testing.T) {
	cfg = Config{
		NetworkModes: map[string]NetworkModeConfig{
			"offline":  {Label: "Offline", Bridge: "vmbr1"},
			"internet": {Label: "Internet (public-only)", Bridge: "vmbr2", RequireEphemeral: true, SandboxOnly: true},
		},
	}
	cfg.Defaults.NetworkMode = "offline"

	_, _, err := validatedNetworkMode(LaunchForm{
		NetworkMode: "internet",
		Ephemeral:   false,
	}, "sandbox")
	if err == nil {
		t.Fatal("validatedNetworkMode succeeded, want error")
	}
	if !strings.Contains(err.Error(), "requires ephemeral") {
		t.Fatalf("error = %q, want ephemeral requirement", err.Error())
	}
}

func TestExtractVMsMergesMetadata(t *testing.T) {
	cfg = Config{
		NetworkModes: map[string]NetworkModeConfig{
			"offline": {Label: "Offline", Bridge: "vmbr1"},
		},
	}
	cfg.Defaults.NetworkMode = "offline"
	cfg.Defaults.InstanceType = "sandbox-medium"
	cfg.Defaults.Bridge = "vmbr1"
	cfg.AllowedTemplates = []struct {
		Name string `json:"name"`
		VMID int    `json:"vmid"`
	}{
		{Name: "debian13-sandbox", VMID: 8010},
	}

	show := map[string]any{
		"values": map[string]any{
			"root_module": map[string]any{
				"resources": []any{
					map[string]any{
						"type": "proxmox_virtual_environment_vm",
						"values": map[string]any{
							"name":  "detonation-01",
							"vm_id": float64(501),
						},
					},
				},
			},
		},
	}

	got := extractVMs(show, map[string]VMMetadata{
		"detonation-01": {
			TemplateVMID: 8030,
			InstanceType: "sandbox-large",
			Bridge:       "vmbr2",
			NetworkMode:  "offline",
		},
	})

	vm, ok := got["detonation-01"]
	if !ok {
		t.Fatal("detonation-01 missing from extractVMs result")
	}
	if vm.VMID != 501 || vm.TemplateVMID != 8030 || vm.InstanceType != "sandbox-large" || vm.Bridge != "vmbr2" {
		t.Fatalf("unexpected VM entry: %#v", vm)
	}
}

func TestTemplateGuestFamily(t *testing.T) {
	cfg = Config{
		AllowedTemplates: []struct {
			Name string `json:"name"`
			VMID int    `json:"vmid"`
		}{
			{Name: "debian13-sandbox", VMID: 8010},
			{Name: "win11-sandbox", VMID: 8030},
		},
	}

	if got := templateGuestFamily(8010); got != "linux" {
		t.Fatalf("templateGuestFamily(8010) = %q, want linux", got)
	}
	if got := templateGuestFamily(8030); got != "windows" {
		t.Fatalf("templateGuestFamily(8030) = %q, want windows", got)
	}
}

func TestMarshalExecArgs(t *testing.T) {
	got := string(marshalExecArgs("qm", "guest", "exec", "8050", "--", "python3", "-c", "print('ok')"))
	want := "qm\x00guest\x00exec\x008050\x00--\x00python3\x00-c\x00print('ok')\x00"
	if got != want {
		t.Fatalf("marshalExecArgs() = %q, want %q", got, want)
	}
}
