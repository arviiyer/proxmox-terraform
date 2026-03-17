package main

import (
	"testing"
)

// buildShowJSON builds a terraform show -json structure for testing.
func buildShowJSON(vms []map[string]any) map[string]any {
	resources := make([]any, 0, len(vms))
	for _, vm := range vms {
		resources = append(resources, map[string]any{
			"type":   "proxmox_virtual_environment_vm",
			"values": vm,
		})
	}
	return map[string]any{
		"values": map[string]any{
			"root_module": map[string]any{
				"resources": resources,
			},
		},
	}
}

func TestExtractVMsFromShow(t *testing.T) {
	tests := []struct {
		name string
		show map[string]any
		want map[string]int
	}{
		{
			name: "normal state",
			show: buildShowJSON([]map[string]any{
				{"name": "vm-01", "vm_id": float64(200), "node_name": "summerset", "ipv4_addresses": []any{}},
				{"name": "vm-02", "vm_id": float64(201), "node_name": "summerset", "ipv4_addresses": []any{}},
			}),
			want: map[string]int{"vm-01": 200, "vm-02": 201},
		},
		{
			name: "empty state",
			show: buildShowJSON([]map[string]any{}),
			want: map[string]int{},
		},
		{
			name: "missing values key (fresh state)",
			show: map[string]any{},
			want: map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVMsFromShow(tt.show)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries, want %d: %v", len(got), len(tt.want), got)
			}
			for name, vmid := range tt.want {
				if got[name] != vmid {
					t.Errorf("got[%q] = %d, want %d", name, got[name], vmid)
				}
			}
		})
	}
}

func TestParseInstancesFromShow(t *testing.T) {
	show := buildShowJSON([]map[string]any{
		{
			"name":      "vm-01",
			"vm_id":     float64(200),
			"node_name": "summerset",
			"ipv4_addresses": []any{
				[]any{"127.0.0.1"},       // loopback, should be skipped
				[]any{"10.0.0.5"},        // match
			},
		},
		{
			"name":           "vm-02",
			"vm_id":          float64(201),
			"node_name":      "summerset",
			"ipv4_addresses": []any{},
		},
	})

	instances := parseInstancesFromShow(show)
	if len(instances) != 2 {
		t.Fatalf("got %d instances, want 2", len(instances))
	}
	if instances[0].Name != "vm-01" || instances[0].VMID != 200 || instances[0].PrivateIP != "10.0.0.5" {
		t.Errorf("unexpected instance[0]: %+v", instances[0])
	}
	if instances[1].Name != "vm-02" || instances[1].PrivateIP != "" {
		t.Errorf("unexpected instance[1]: %+v", instances[1])
	}
}

func TestMergeVMs(t *testing.T) {
	tests := []struct {
		name     string
		existing map[string]int
		incoming map[string]int
		wantErr  string
		wantLen  int
	}{
		{
			name:     "fresh state, no conflicts",
			existing: map[string]int{},
			incoming: map[string]int{"vm-01": 200, "vm-02": 201},
			wantLen:  2,
		},
		{
			name:     "existing vms, no conflicts",
			existing: map[string]int{"vm-01": 200},
			incoming: map[string]int{"vm-02": 201},
			wantLen:  2,
		},
		{
			name:     "name conflict",
			existing: map[string]int{"vm-01": 200},
			incoming: map[string]int{"vm-01": 201},
			wantErr:  `VM name "vm-01" already exists in state`,
		},
		{
			name:     "vmid conflict",
			existing: map[string]int{"vm-01": 200},
			incoming: map[string]int{"vm-02": 200},
			wantErr:  `VMID 200 is already used by "vm-01"`,
		},
		{
			name:     "merged map contains both existing and incoming",
			existing: map[string]int{"vm-01": 200},
			incoming: map[string]int{"vm-02": 201},
			wantLen:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeVMs(tt.existing, tt.incoming)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("got error %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("got %d entries, want %d: %v", len(got), tt.wantLen, got)
			}
			for k, v := range tt.existing {
				if got[k] != v {
					t.Errorf("existing key %q: got %d, want %d", k, got[k], v)
				}
			}
			for k, v := range tt.incoming {
				if got[k] != v {
					t.Errorf("incoming key %q: got %d, want %d", k, got[k], v)
				}
			}
		})
	}
}
