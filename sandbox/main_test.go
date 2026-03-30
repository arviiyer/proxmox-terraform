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

func TestShellQuote(t *testing.T) {
	got := shellQuote("qm", "guest", "exec", "8050", "--", "python3", "-c", "print('ok')")
	want := `'qm' 'guest' 'exec' '8050' '--' 'python3' '-c' 'print('\''ok'\'')'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestVMStatusRunning(t *testing.T) {
	if !vmStatusRunning("status: running\n") {
		t.Fatal("vmStatusRunning returned false for running status")
	}
	if vmStatusRunning("status: stopped\n") {
		t.Fatal("vmStatusRunning returned true for stopped status")
	}
}

func TestGuestInterfacesReady(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   bool
		hasErr bool
	}{
		{
			name: "ready with ipv4",
			input: `[{"name":"Ethernet","ip-addresses":[{"ip-address":"10.0.2.101","ip-address-type":"ipv4"}]}]`,
			want: true,
		},
		{
			name: "loopback only",
			input: `[{"name":"lo","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4"}]}]`,
			want: false,
		},
		{
			name: "non-loopback without ipv4",
			input: `[{"name":"eth0","ip-addresses":[{"ip-address":"fe80::1","ip-address-type":"ipv6"}]}]`,
			want: false,
		},
		{
			name:   "invalid json",
			input:  `{`,
			hasErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := guestInterfacesReady(tc.input)
			if tc.hasErr {
				if err == nil {
					t.Fatal("guestInterfacesReady succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("guestInterfacesReady returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("guestInterfacesReady() = %v, want %v", got, tc.want)
			}
		})
	}
}
