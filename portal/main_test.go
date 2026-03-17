package main

import (
	"testing"
)

func TestExtractVMsFromOutput(t *testing.T) {
	tests := []struct {
		name string
		out  map[string]any
		want map[string]int
	}{
		{
			name: "normal output",
			out: map[string]any{
				"instances": map[string]any{
					"value": []any{
						map[string]any{"name": "vm-01", "vm_id": float64(200), "node": "summerset", "private_ip": "10.0.0.1"},
						map[string]any{"name": "vm-02", "vm_id": float64(201), "node": "summerset", "private_ip": nil},
					},
				},
			},
			want: map[string]int{"vm-01": 200, "vm-02": 201},
		},
		{
			name: "empty instances",
			out: map[string]any{
				"instances": map[string]any{
					"value": []any{},
				},
			},
			want: map[string]int{},
		},
		{
			name: "missing instances key (fresh state)",
			out:  map[string]any{},
			want: map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVMsFromOutput(tt.out)
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
			// Verify all existing and incoming keys are present
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
