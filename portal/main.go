package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	tf "github.com/arviiyer/proxmox-terraform/portal/internal/terraform"
)

//go:embed web/templates/*.html
var templatesFS embed.FS

type Config struct {
	TerraformDir     string `json:"terraform_dir"`
	AllowedTemplates []struct {
		Name string `json:"name"`
		VMID int    `json:"vmid"`
	} `json:"allowed_templates"`
	AllowedInstanceTypes []string `json:"allowed_instance_types"`
	Defaults             struct {
		NodeName     string `json:"node_name"`
		Bridge       string `json:"bridge"`
		CIUser       string `json:"ci_user"`
		CIDatastore  string `json:"ci_datastore"`
		FullClone    bool   `json:"full_clone"`
		InstanceType string `json:"instance_type"`
	} `json:"defaults"`
}

type LaunchForm struct {
	TemplateVMID int
	InstanceType string
	Count        int
	NamePrefix   string
	VMIDStart    int
	FullClone    bool
}

type Result struct {
	Logs          string
	InstancesJSON string
	Error         string
}

type Instance struct {
	Name      string
	VMID      int
	Node      string
	PrivateIP string
}

var (
	cfg          Config
	tmpl         *template.Template
	applyLock    sync.Mutex // prevent concurrent applies against same local state
	sshPublicKey string
	pveEndpoint  string
	pveAPIToken  string
)

func main() {
	// Load config.json
	b, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatalf("read config.json: %v", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalf("parse config.json: %v", err)
	}

	sshPublicKey = os.Getenv("SSH_PUBLIC_KEY")
	if sshPublicKey == "" {
		log.Fatal("SSH_PUBLIC_KEY env var is required")
	}
	pveEndpoint = os.Getenv("PVE_ENDPOINT")
	if pveEndpoint == "" {
		log.Fatal("PVE_ENDPOINT env var is required")
	}
	pveAPIToken = os.Getenv("PVE_API_TOKEN")
	if pveAPIToken == "" {
		log.Fatal("PVE_API_TOKEN env var is required")
	}

	tmpl = template.Must(template.ParseFS(
		templatesFS,
		"web/templates/index.html",
		"web/templates/result.html",
	))

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/launch", handleLaunch)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("ok")) })

	addr := ":8088"
	log.Printf("portal listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	var instances []Instance
	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := tf.DefaultTimeoutCtx()
	defer cancel()
	if out, err := runner.OutputJSON(ctx); err == nil {
		instances = parseInstances(out)
	}

	data := map[string]any{
		"templates":     cfg.AllowedTemplates,
		"instanceTypes": cfg.AllowedInstanceTypes,
		"defaults":      cfg.Defaults,
		"instances":     instances,
	}
	_ = tmpl.ExecuteTemplate(w, "index.html", data)
}

func handleLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}

	form, err := parseLaunchForm(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	// Guardrails: allowlist template + instance types
	if !isAllowedTemplate(form.TemplateVMID) {
		http.Error(w, "template not allowed", 400)
		return
	}
	if !isAllowedInstanceType(form.InstanceType) {
		http.Error(w, "instance type not allowed", 400)
		return
	}
	if form.Count < 1 || form.Count > 50 {
		http.Error(w, "count must be 1..50", 400)
		return
	}
	if form.VMIDStart < 100 || form.VMIDStart > 999999 {
		http.Error(w, "vmid_start out of range", 400)
		return
	}
	if strings.TrimSpace(form.NamePrefix) == "" {
		http.Error(w, "name_prefix required", 400)
		return
	}

	applyLock.Lock()
	defer applyLock.Unlock()

	runner := tf.Runner{Dir: cfg.TerraformDir}

	ctx, cancel := tf.DefaultTimeoutCtx()
	defer cancel()

	// Init (idempotent)
	if err := runner.Init(ctx); err != nil {
		renderResult(w, Result{Error: err.Error()})
		return
	}

	// Read existing state so we can merge rather than replace
	existingVMs := map[string]int{}
	if existing, err := runner.OutputJSON(ctx); err == nil {
		existingVMs = extractVMsFromOutput(existing)
	}

	// Build new vms map: name -> vmid
	newVMs := map[string]int{}
	for i := 0; i < form.Count; i++ {
		name := fmt.Sprintf("%s-%02d", form.NamePrefix, i+1)
		newVMs[name] = form.VMIDStart + i
	}

	// Conflict check + merge
	vms, err := mergeVMs(existingVMs, newVMs)
	if err != nil {
		renderResult(w, Result{Error: err.Error()})
		return
	}

	// Create var-file payload matching your Terraform variables
	varPayload := map[string]any{
		"template_vmid": form.TemplateVMID,
		"instance_type": form.InstanceType,
		"full_clone":    form.FullClone,
		"vms":           vms,

		"node_name":      cfg.Defaults.NodeName,
		"bridge":         cfg.Defaults.Bridge,
		"ci_user":        cfg.Defaults.CIUser,
		"ci_datastore":   cfg.Defaults.CIDatastore,
		"ssh_public_key": sshPublicKey,
		"pve_endpoint":   pveEndpoint,
		"pve_api_token":  pveAPIToken,
	}

	varFile, err := tf.WriteVarFileJSON(cfg.TerraformDir, varPayload)
	if err != nil {
		renderResult(w, Result{Error: err.Error()})
		return
	}

	logs, err := runner.Apply(ctx, varFile)
	logs = stripANSI(logs)

	if err != nil {
		renderResult(w, Result{Logs: logs, Error: err.Error()})
		return
	}

	// Refresh-only to populate IPs (DHCP discovery lag)
	_, _ = runner.RefreshOnly(ctx, varFile)

	out, err := runner.OutputJSON(ctx)
	if err != nil {
		renderResult(w, Result{Logs: logs, Error: err.Error()})
		return
	}

	outMeta, _ := out["instances"].(map[string]any)
	instances := outMeta["value"]

	pretty := ""
	if b, err := json.MarshalIndent(instances, "", "  "); err == nil {
		pretty = string(b)
	} else {
		pretty = fmt.Sprintf("%v", instances)
	}

	renderResult(w, Result{Logs: logs, InstancesJSON: pretty})
}

func parseLaunchForm(r *http.Request) (LaunchForm, error) {
	tpl, err := strconv.Atoi(r.FormValue("template_vmid"))
	if err != nil {
		return LaunchForm{}, fmt.Errorf("invalid template_vmid: %w", err)
	}
	count, err := strconv.Atoi(r.FormValue("count"))
	if err != nil {
		return LaunchForm{}, fmt.Errorf("invalid count: %w", err)
	}
	vmidStart, err := strconv.Atoi(r.FormValue("vmid_start"))
	if err != nil {
		return LaunchForm{}, fmt.Errorf("invalid vmid_start: %w", err)
	}

	return LaunchForm{
		TemplateVMID: tpl,
		InstanceType: r.FormValue("instance_type"),
		Count:        count,
		NamePrefix:   r.FormValue("name_prefix"),
		VMIDStart:    vmidStart,
		FullClone:    r.FormValue("full_clone") == "on",
	}, nil
}

func isAllowedTemplate(vmid int) bool {
	for _, t := range cfg.AllowedTemplates {
		if t.VMID == vmid {
			return true
		}
	}
	return false
}

func isAllowedInstanceType(s string) bool {
	for _, it := range cfg.AllowedInstanceTypes {
		if it == s {
			return true
		}
	}
	return false
}

func renderResult(w http.ResponseWriter, res Result) {
	_ = tmpl.ExecuteTemplate(w, "result.html", res)
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// mergeVMs checks newVMs for name/VMID conflicts with existingVMs, then returns
// the merged map (existing + new) to be passed to Terraform.
func mergeVMs(existing, incoming map[string]int) (map[string]int, error) {
	for name, vmid := range incoming {
		if _, exists := existing[name]; exists {
			return nil, fmt.Errorf("VM name %q already exists in state", name)
		}
		for existingName, existingVMID := range existing {
			if existingVMID == vmid {
				return nil, fmt.Errorf("VMID %d is already used by %q", vmid, existingName)
			}
		}
	}
	merged := make(map[string]int, len(existing)+len(incoming))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range incoming {
		merged[k] = v
	}
	return merged, nil
}

// parseInstances extracts the full instance list from terraform output JSON.
func parseInstances(out map[string]any) []Instance {
	meta, _ := out["instances"].(map[string]any)
	items, _ := meta["value"].([]any)
	result := make([]Instance, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		vmidF, _ := m["vm_id"].(float64)
		node, _ := m["node"].(string)
		ip, _ := m["private_ip"].(string)
		if name != "" {
			result = append(result, Instance{
				Name:      name,
				VMID:      int(vmidF),
				Node:      node,
				PrivateIP: ip,
			})
		}
	}
	return result
}

// extractVMsFromOutput rebuilds a name->vmid map from terraform output JSON.
func extractVMsFromOutput(out map[string]any) map[string]int {
	result := map[string]int{}
	meta, _ := out["instances"].(map[string]any)
	items, _ := meta["value"].([]any)
	for _, item := range items {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		vmidF, _ := m["vm_id"].(float64)
		if name != "" {
			result[name] = int(vmidF)
		}
	}
	return result
}
