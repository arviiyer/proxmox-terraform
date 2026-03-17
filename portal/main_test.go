package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"color code", "\x1b[32mgreen\x1b[0m", "green"},
		{"bold", "\x1b[1mbold\x1b[0m", "bold"},
		{"erase line", "foo\x1b[2Kbar", "foobar"},
		{"cursor up", "foo\x1b[Abar", "foobar"},
		{"no escapes", "plain text", "plain text"},
		{"mixed", "\x1b[1;32mok\x1b[0m normal \x1b[2K", "ok normal "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripANSI(tt.input); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLaunchForm(t *testing.T) {
	makeRequest := func(vals map[string]string) *http.Request {
		form := url.Values{}
		for k, v := range vals {
			form.Set(k, v)
		}
		r, _ := http.NewRequest(http.MethodPost, "/launch", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}

	valid := map[string]string{
		"template_vmid": "8000",
		"instance_type": "general-small",
		"count":         "2",
		"vmid_start":    "200",
		"name_prefix":   "vm",
		"full_clone":    "on",
	}

	t.Run("valid form", func(t *testing.T) {
		r := makeRequest(valid)
		r.ParseForm()
		f, err := parseLaunchForm(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.TemplateVMID != 8000 || f.Count != 2 || f.VMIDStart != 200 || !f.FullClone {
			t.Errorf("unexpected form values: %+v", f)
		}
	})

	t.Run("invalid template_vmid", func(t *testing.T) {
		m := copyMap(valid)
		m["template_vmid"] = "abc"
		r := makeRequest(m)
		r.ParseForm()
		_, err := parseLaunchForm(r)
		if err == nil || !strings.Contains(err.Error(), "template_vmid") {
			t.Errorf("expected template_vmid error, got %v", err)
		}
	})

	t.Run("invalid count", func(t *testing.T) {
		m := copyMap(valid)
		m["count"] = "xyz"
		r := makeRequest(m)
		r.ParseForm()
		_, err := parseLaunchForm(r)
		if err == nil || !strings.Contains(err.Error(), "count") {
			t.Errorf("expected count error, got %v", err)
		}
	})

	t.Run("invalid vmid_start", func(t *testing.T) {
		m := copyMap(valid)
		m["vmid_start"] = "bad"
		r := makeRequest(m)
		r.ParseForm()
		_, err := parseLaunchForm(r)
		if err == nil || !strings.Contains(err.Error(), "vmid_start") {
			t.Errorf("expected vmid_start error, got %v", err)
		}
	})

	t.Run("full_clone off when not set", func(t *testing.T) {
		m := copyMap(valid)
		delete(m, "full_clone")
		r := makeRequest(m)
		r.ParseForm()
		f, err := parseLaunchForm(r)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.FullClone {
			t.Error("expected FullClone=false when checkbox absent")
		}
	})
}

func TestLoadSaveProtected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "protected.json")

	t.Run("missing file returns empty map", func(t *testing.T) {
		// point loadProtected at a non-existent file by cd-ing... instead
		// call the helpers directly with a temp path via the OS
		orig, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(orig)

		got := loadProtected()
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})

	t.Run("save and reload", func(t *testing.T) {
		orig, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(orig)

		saveProtected(map[string]bool{"vm-01": true, "vm-02": true})
		got := loadProtected()
		if !got["vm-01"] || !got["vm-02"] || len(got) != 2 {
			t.Errorf("unexpected protected set: %v", got)
		}
		_ = path // suppress unused warning
	})

	t.Run("toggle adds then removes", func(t *testing.T) {
		orig, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(orig)

		saveProtected(map[string]bool{})

		// add
		p := loadProtected()
		p["vm-01"] = true
		saveProtected(p)
		if got := loadProtected(); !got["vm-01"] {
			t.Error("expected vm-01 to be protected")
		}

		// remove
		p = loadProtected()
		delete(p, "vm-01")
		saveProtected(p)
		if got := loadProtected(); got["vm-01"] {
			t.Error("expected vm-01 to be unprotected")
		}
	})
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

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
