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

	pve "github.com/arviiyer/proxmox-terraform/portal/internal/proxmox"
	tf "github.com/arviiyer/proxmox-terraform/portal/internal/terraform"
)

//go:embed web/templates/*.html
var templatesFS embed.FS

type Config struct {
	TerraformDir     string   `json:"terraform_dir"`
	AllowedNodes     []string `json:"allowed_nodes"`
	AllowedTemplates []struct {
		Name string `json:"name"`
		VMID int    `json:"vmid"`
	} `json:"allowed_templates"`
	AllowedInstanceTypes []string `json:"allowed_instance_types"`
	Defaults             struct {
		NodeName     string `json:"node_name"`
		TemplateNode string `json:"template_node"`
		Bridge       string `json:"bridge"`
		CIUser       string `json:"ci_user"`
		CIDatastore  string `json:"ci_datastore"`
		FullClone    bool   `json:"full_clone"`
		InstanceType string `json:"instance_type"`
	} `json:"defaults"`
}

// VMEntry is the per-VM configuration passed to Terraform.
type VMEntry struct {
	VMID int    `json:"vmid"`
	Node string `json:"node"`
}

type LaunchForm struct {
	TemplateVMID int
	InstanceType string
	Node         string
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
	Status    string // "running", "stopped", or "" if unknown
}

const protectedConfirmPhrase = "destroy protected instance"

var (
	cfg           Config
	tmpl          *template.Template
	applyLock     sync.Mutex // prevent concurrent applies against same local state
	sshPublicKey  string
	pveEndpoint   string
	pveAPIToken   string
	protectedLock sync.Mutex
	pveClient     *pve.Client
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

	pveClient = pve.New(pveEndpoint, pveAPIToken)

	tmpl = template.Must(template.ParseFS(
		templatesFS,
		"web/templates/index.html",
		"web/templates/result.html",
	))

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/launch", handleLaunch)
	http.HandleFunc("/destroy", handleDestroy)
	http.HandleFunc("/start", handleStart)
	http.HandleFunc("/stop", handleStop)
	http.HandleFunc("/toggle-protection", handleToggleProtection)
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
	if show, err := runner.ShowJSON(ctx); err == nil {
		instances = parseInstancesFromShow(show)
	}

	// Fetch VM power states in parallel — best effort, unknown on error.
	var wg sync.WaitGroup
	for i := range instances {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, err := pveClient.VMStatus(instances[i].Node, instances[i].VMID)
			if err == nil {
				instances[i].Status = status
			}
		}(i)
	}
	wg.Wait()

	data := map[string]any{
		"templates":     cfg.AllowedTemplates,
		"instanceTypes": cfg.AllowedInstanceTypes,
		"nodes":         cfg.AllowedNodes,
		"defaults":      cfg.Defaults,
		"instances":     instances,
		"protected":     loadProtected(),
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

	// Guardrails: allowlist template, instance type, node
	if !isAllowedTemplate(form.TemplateVMID) {
		http.Error(w, "template not allowed", 400)
		return
	}
	if !isAllowedInstanceType(form.InstanceType) {
		http.Error(w, "instance type not allowed", 400)
		return
	}
	if !isAllowedNode(form.Node) {
		http.Error(w, "node not allowed", 400)
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
	existingVMs := map[string]VMEntry{}
	if show, err := runner.ShowJSON(ctx); err == nil {
		existingVMs = extractVMsFromShow(show)
	}

	// Build new vms map: name -> VMEntry
	newVMs := map[string]VMEntry{}
	for i := 0; i < form.Count; i++ {
		name := fmt.Sprintf("%s-%02d", form.NamePrefix, i+1)
		newVMs[name] = VMEntry{VMID: form.VMIDStart + i, Node: form.Node}
	}

	// Conflict check + merge
	vms, err := mergeVMs(existingVMs, newVMs)
	if err != nil {
		renderResult(w, Result{Error: err.Error()})
		return
	}

	// Create var-file payload matching Terraform variables
	varPayload := map[string]any{
		"template_vmid": form.TemplateVMID,
		"template_node": cfg.Defaults.TemplateNode,
		"instance_type": form.InstanceType,
		"full_clone":    form.FullClone,
		"vms":           vms,

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
		Node:         r.FormValue("node"),
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

func isAllowedNode(s string) bool {
	for _, n := range cfg.AllowedNodes {
		if n == s {
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
func mergeVMs(existing, incoming map[string]VMEntry) (map[string]VMEntry, error) {
	for name, entry := range incoming {
		if _, exists := existing[name]; exists {
			return nil, fmt.Errorf("VM name %q already exists in state", name)
		}
		for existingName, existingEntry := range existing {
			if existingEntry.VMID == entry.VMID {
				return nil, fmt.Errorf("VMID %d is already used by %q", entry.VMID, existingName)
			}
		}
	}
	merged := make(map[string]VMEntry, len(existing)+len(incoming))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range incoming {
		merged[k] = v
	}
	return merged, nil
}

// showResources returns all proxmox VM resource values from terraform show -json.
func showResources(show map[string]any) []map[string]any {
	values, _ := show["values"].(map[string]any)
	root, _ := values["root_module"].(map[string]any)
	resources, _ := root["resources"].([]any)
	var result []map[string]any
	for _, r := range resources {
		rm, _ := r.(map[string]any)
		if rm["type"] == "proxmox_virtual_environment_vm" {
			result = append(result, rm)
		}
	}
	return result
}

// parseInstancesFromShow extracts the full instance list from terraform show -json.
func parseInstancesFromShow(show map[string]any) []Instance {
	var result []Instance
	for _, r := range showResources(show) {
		vals, _ := r["values"].(map[string]any)
		name, _ := vals["name"].(string)
		vmidF, _ := vals["vm_id"].(float64)
		node, _ := vals["node_name"].(string)
		if name == "" {
			continue
		}
		result = append(result, Instance{
			Name:      name,
			VMID:      int(vmidF),
			Node:      node,
			PrivateIP: extractIPFromShow(vals),
		})
	}
	return result
}

// extractIPFromShow finds the first 10.x IP from ipv4_addresses in show output.
func extractIPFromShow(vals map[string]any) string {
	addrs, _ := vals["ipv4_addresses"].([]any)
	for _, iface := range addrs {
		ips, _ := iface.([]any)
		for _, ip := range ips {
			if s, _ := ip.(string); strings.HasPrefix(s, "10.") {
				return s
			}
		}
	}
	return ""
}

// handleDestroy destroys a single VM by name after confirmation.
func handleDestroy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	confirm := r.FormValue("confirm")
	overrideConfirm := r.FormValue("override_confirm")

	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	if confirm != name {
		http.Error(w, "confirmation does not match instance name", 400)
		return
	}

	protected := loadProtected()
	if protected[name] && overrideConfirm != protectedConfirmPhrase {
		http.Error(w, fmt.Sprintf("instance is protected; type %q to proceed", protectedConfirmPhrase), 400)
		return
	}

	applyLock.Lock()
	defer applyLock.Unlock()

	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := tf.DefaultTimeoutCtx()
	defer cancel()

	// Build var file with current state so credentials are present.
	existingVMs := map[string]VMEntry{}
	if show, err := runner.ShowJSON(ctx); err == nil {
		existingVMs = extractVMsFromShow(show)
	}
	varPayload := map[string]any{
		"vms":            existingVMs,
		"template_vmid":  cfg.AllowedTemplates[0].VMID,
		"template_node":  cfg.Defaults.TemplateNode,
		"instance_type":  cfg.Defaults.InstanceType,
		"full_clone":     cfg.Defaults.FullClone,
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

	target := fmt.Sprintf("proxmox_virtual_environment_vm.vm[%q]", name)
	logs, err := runner.Destroy(ctx, target, varFile)
	logs = stripANSI(logs)
	if err != nil {
		renderResult(w, Result{Logs: logs, Error: err.Error()})
		return
	}

	renderResult(w, Result{Logs: logs})
}

// handleToggleProtection adds or removes a VM from the protected list.
func handleToggleProtection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}

	protectedLock.Lock()
	protected := loadProtected()
	if protected[name] {
		delete(protected, name)
	} else {
		protected[name] = true
	}
	saveProtected(protected)
	protectedLock.Unlock()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// loadProtected reads protected.json and returns a set of protected VM names.
// Returns an empty map if the file does not exist.
func loadProtected() map[string]bool {
	b, err := os.ReadFile("protected.json")
	if err != nil {
		return map[string]bool{}
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// saveProtected writes the protected set to protected.json.
func saveProtected(protected map[string]bool) {
	names := make([]string, 0, len(protected))
	for n := range protected {
		names = append(names, n)
	}
	b, _ := json.MarshalIndent(names, "", "  ")
	_ = os.WriteFile("protected.json", b, 0o600)
}

// handleStart powers on a stopped VM via the Proxmox API.
func handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}

	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := tf.DefaultTimeoutCtx()
	defer cancel()
	vms := map[string]VMEntry{}
	if show, err := runner.ShowJSON(ctx); err == nil {
		vms = extractVMsFromShow(show)
	}
	entry, ok := vms[name]
	if !ok {
		http.Error(w, "instance not found in state", 404)
		return
	}

	if err := pveClient.StartVM(entry.Node, entry.VMID); err != nil {
		http.Error(w, fmt.Sprintf("start failed: %v", err), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleStop sends an ACPI shutdown to a running VM via the Proxmox API.
func handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}

	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := tf.DefaultTimeoutCtx()
	defer cancel()
	vms := map[string]VMEntry{}
	if show, err := runner.ShowJSON(ctx); err == nil {
		vms = extractVMsFromShow(show)
	}
	entry, ok := vms[name]
	if !ok {
		http.Error(w, "instance not found in state", 404)
		return
	}

	if err := pveClient.StopVM(entry.Node, entry.VMID); err != nil {
		http.Error(w, fmt.Sprintf("stop failed: %v", err), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// extractVMsFromShow rebuilds a name->VMEntry map from terraform show -json.
func extractVMsFromShow(show map[string]any) map[string]VMEntry {
	result := map[string]VMEntry{}
	for _, r := range showResources(show) {
		vals, _ := r["values"].(map[string]any)
		name, _ := vals["name"].(string)
		vmidF, _ := vals["vm_id"].(float64)
		node, _ := vals["node_name"].(string)
		if name != "" {
			result[name] = VMEntry{VMID: int(vmidF), Node: node}
		}
	}
	return result
}
