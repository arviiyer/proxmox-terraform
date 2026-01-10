package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	tf "github.com/arviiyer/proxmox-terraform/portal/internal/terraform"
)

//go:embed web/templates/*.html
var templatesFS embed.FS

type Config struct {
	TerraformDir          string `json:"terraform_dir"`
	AllowedTemplates      []struct {
		Name string `json:"name"`
		VMID int    `json:"vmid"`
	} `json:"allowed_templates"`
	AllowedInstanceTypes []string `json:"allowed_instance_types"`
	Defaults             struct {
		NodeName      string `json:"node_name"`
		Bridge        string `json:"bridge"`
		CIUser        string `json:"ci_user"`
		CIDatastore   string `json:"ci_datastore"`
		FullClone     bool   `json:"full_clone"`
		InstanceType  string `json:"instance_type"`
	} `json:"defaults"`
}

type LaunchForm struct {
	TemplateVMID  int
	InstanceType  string
	Count         int
	NamePrefix    string
	VMIDStart     int
	FullClone     bool
}

type Result struct {
	Logs      string
	Instances any
	Error     string
}

var (
	cfg       Config
	tmpl      *template.Template
	applyLock sync.Mutex // prevent concurrent applies against same local state
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

	data := map[string]any{
		"templates":     cfg.AllowedTemplates,
		"instanceTypes": cfg.AllowedInstanceTypes,
		"defaults":      cfg.Defaults,
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

	// Build vms map: name -> vmid
	vms := map[string]int{}
	for i := 0; i < form.Count; i++ {
		name := fmt.Sprintf("%s-%02d", form.NamePrefix, i+1)
		vms[name] = form.VMIDStart + i
	}

	// Create var-file payload matching your Terraform variables
	varPayload := map[string]any{
		"template_vmid":  form.TemplateVMID,
		"instance_type":  form.InstanceType,
		"full_clone":     form.FullClone,
		"vms":            vms,

		// optional pass-throughs so portal can set defaults without editing tfvars
		"node_name":      cfg.Defaults.NodeName,
		"bridge":         cfg.Defaults.Bridge,
		"ci_user":        cfg.Defaults.CIUser,
		"ci_datastore":   cfg.Defaults.CIDatastore,
		// ssh_public_key should come from env or keep in tfvars; if you want portal to set it, add it here.
	}

	varFile, err := tf.WriteVarFileJSON(cfg.TerraformDir, varPayload)
	if err != nil {
		renderResult(w, Result{Error: err.Error()})
		return
	}

	logs, err := runner.Apply(ctx, varFile)
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

	instances := out["instances"]
	renderResult(w, Result{Logs: logs, Instances: instances})
}

func parseLaunchForm(r *http.Request) (LaunchForm, error) {
	tpl, _ := strconv.Atoi(r.FormValue("template_vmid"))
	count, _ := strconv.Atoi(r.FormValue("count"))
	vmidStart, _ := strconv.Atoi(r.FormValue("vmid_start"))
	fullClone := r.FormValue("full_clone") == "on"

	return LaunchForm{
		TemplateVMID: tpl,
		InstanceType: r.FormValue("instance_type"),
		Count:        count,
		NamePrefix:   r.FormValue("name_prefix"),
		VMIDStart:    vmidStart,
		FullClone:    fullClone,
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
