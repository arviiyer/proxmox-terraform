package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	pve "github.com/arviiyer/proxmox-terraform/portal/proxmox"
	tf "github.com/arviiyer/proxmox-terraform/portal/terraform"
)

//go:embed web/templates/*.html
var templatesFS embed.FS

// Config loaded from config.json.
type Config struct {
	TerraformDir     string                       `json:"terraform_dir"`
	NetworkModes     map[string]NetworkModeConfig `json:"network_modes"`
	AllowedTemplates []struct {
		Name string `json:"name"`
		VMID int    `json:"vmid"`
	} `json:"allowed_templates"`
	AllowedInstanceTypes []string `json:"allowed_instance_types"`
	Defaults             struct {
		NodeName     string `json:"node_name"`
		TemplateNode string `json:"template_node"`
		Bridge       string `json:"bridge"`
		NetworkMode  string `json:"network_mode"`
		Datastore    string `json:"datastore"`
		InstanceType string `json:"instance_type"`
	} `json:"defaults"`
}

type NetworkModeConfig struct {
	Label            string `json:"label"`
	Bridge           string `json:"bridge"`
	Description      string `json:"description"`
	SandboxOnly      bool   `json:"sandbox_only"`
	RequireEphemeral bool   `json:"require_ephemeral"`
	DNSServer        string `json:"dns_server"`
}

type LaunchTemplate struct {
	Name        string
	Label       string
	Description string
	VMID        int
}

type NetworkModeOption struct {
	Name             string
	Label            string
	Description      string
	Bridge           string
	RequireEphemeral bool
}

// VMEntry is passed to Terraform as the vms variable.
type VMEntry struct {
	VMID         int    `json:"vmid"`
	TemplateVMID int    `json:"template_vmid"`
	InstanceType string `json:"instance_type"`
	Bridge       string `json:"bridge"`
}

// Instance is what we display on the dashboard.
type Instance struct {
	Name      string
	VMID      int
	Node      string
	OS        string
	View      string
	Status    string // "running", "stopped", ""
	Ephemeral bool
	Network   string
}

// LaunchForm holds parsed POST /launch parameters.
type LaunchForm struct {
	TemplateVMID int
	InstanceType string
	Count        int
	NamePrefix   string
	VMIDStart    int
	Ephemeral    bool
	NetworkMode  string
}

type URLSubmissionForm struct {
	URL          string
	TemplateVMID int
	InstanceType string
	NamePrefix   string
	VMIDStart    int
	NetworkMode  string
}

// Job tracks an in-progress Terraform apply so the dashboard can show a banner.
type Job struct {
	ID            string
	Kind          string // "launch", "destroy", "submit-url"
	Names         []string
	View          string
	OS            string
	Network       string
	SubmissionURL string
	StagedPath    string
	mu            sync.Mutex
	done          bool
	logs          string
	errMsg        string
}

func (j *Job) complete(logs string, err error) {
	j.mu.Lock()
	j.done = true
	j.logs = logs
	if err != nil {
		j.errMsg = err.Error()
	}
	j.mu.Unlock()
}

func (j *Job) snapshot() (status, logs, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.done {
		return "running", "", ""
	}
	if j.errMsg != "" {
		return "failed", j.logs, j.errMsg
	}
	return "done", j.logs, ""
}

func (j *Job) isDone() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

var (
	cfg            Config
	tmpl           *template.Template
	applyLock      sync.Mutex
	pveEndpoint    string
	pveAPIToken    string
	sshNodeKey     string
	sshNodeKeyPath string
	pveClient      *pve.Client
	ephemeralMu    sync.Mutex
	metadataMu     sync.Mutex
	jobsMu         sync.Mutex
	jobMap         = map[string]*Job{}

	// stoppedSince tracks when each ephemeral VM was first seen as stopped.
	// A VM must stay stopped for ephemeralGrace before it is auto-destroyed,
	// to allow Windows VMs to reboot during first-boot setup without being
	// prematurely destroyed.
	stoppedSinceMu sync.Mutex
	stoppedSince   = map[string]time.Time{}
	ephemeralGrace = 2 * time.Minute
)

const (
	postLaunchVMRunningTimeout  = 2 * time.Minute
	postLaunchGuestReadyTimeout = 8 * time.Minute
	postLaunchDNSConfigTimeout  = 2 * time.Minute
	postLaunchCommandTimeout    = 20 * time.Second
	postLaunchPollInterval      = 5 * time.Second
)

func newJob(kind string, names []string) *Job {
	j := &Job{ID: fmt.Sprintf("%d", time.Now().UnixNano()), Kind: kind, Names: names}
	jobsMu.Lock()
	jobMap[j.ID] = j
	jobsMu.Unlock()
	return j
}

func getJob(id string) *Job {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	return jobMap[id]
}

func main() {
	b, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatalf("read config.json: %v", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalf("parse config.json: %v", err)
	}
	if _, _, err := networkModeSpec(cfg.Defaults.NetworkMode); err != nil {
		log.Fatalf("invalid default network mode: %v", err)
	}

	pveEndpoint = os.Getenv("PVE_ENDPOINT")
	if pveEndpoint == "" {
		log.Fatal("PVE_ENDPOINT env var is required")
	}
	pveAPIToken = os.Getenv("PVE_API_TOKEN")
	if pveAPIToken == "" {
		log.Fatal("PVE_API_TOKEN env var is required")
	}

	keyFile := os.Getenv("SSH_NODE_KEY_FILE")
	if keyFile == "" {
		keyFile = os.Getenv("HOME") + "/.ssh/id_ed25519"
	}
	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		log.Fatalf("read SSH node key %q: %v", keyFile, err)
	}
	sshNodeKeyPath = keyFile
	sshNodeKey = string(keyBytes)

	pveClient = pve.New(pveEndpoint, pveAPIToken)

	tmpl = template.Must(template.ParseFS(templatesFS,
		"web/templates/index.html",
		"web/templates/result.html",
		"web/templates/console.html",
		"web/templates/job.html",
	))

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/launch", handleLaunch)
	http.HandleFunc("/submit-url", handleSubmitURL)
	http.HandleFunc("/destroy", handleDestroy)
	http.HandleFunc("/start", handleStart)
	http.HandleFunc("/stop", handleStop)
	http.HandleFunc("/snapshot", handleSnapshot)
	http.HandleFunc("/revert", handleRevert)
	http.HandleFunc("/snapshots/", handleListSnapshots) // GET /snapshots/{name} → JSON
	http.HandleFunc("/job/", handleJob)
	http.HandleFunc("/console/", handleConsole)
	http.HandleFunc("/ws/", handleWS)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Start ephemeral VM watcher.
	go watchEphemeral()

	addr := ":8089"
	log.Printf("sandbox listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// ── Index ──────────────────────────────────────────────────────────────────

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	currentView := viewFromRequest(r.URL.Query().Get("view"))

	var instances []Instance
	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := tf.DefaultTimeoutCtx()
	defer cancel()
	if show, err := runner.ShowJSON(ctx); err == nil {
		instances = parseInstances(show)
	}

	// Fetch power states in parallel.
	ephemeral := loadEphemeral()
	metadata := loadMetadata()
	var wg sync.WaitGroup
	for i := range instances {
		instances[i].Ephemeral = ephemeral[instances[i].Name]
		meta := normalizedMetadata(metadata[instances[i].Name])
		instances[i].OS = templateOSLabel(meta.TemplateVMID)
		instances[i].View = templateView(meta.TemplateVMID)
		instances[i].Network = networkModeLabel(meta.NetworkMode)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := pveClient.VMStatus(instances[i].Node, instances[i].VMID)
			if err == nil {
				instances[i].Status = st
			}
		}(i)
	}
	wg.Wait()

	// Resolve active job from URL — used for the banner.
	var activeJob *Job
	if jobID := r.URL.Query().Get("job"); jobID != "" {
		if j := getJob(jobID); j != nil && !j.isDone() {
			activeJob = j
		}
	}

	// For launch jobs: add placeholder rows for VMs not yet in state.
	if activeJob != nil && activeJob.Kind == "launch" {
		for _, n := range activeJob.Names {
			found := false
			for _, inst := range instances {
				if inst.Name == n {
					found = true
					break
				}
			}
			if !found {
				instances = append(instances, Instance{
					Name:    n,
					OS:      activeJob.OS,
					View:    activeJob.View,
					Network: activeJob.Network,
					Status:  "launching",
				})
			}
		}
	}

	// For destroy jobs: mark the target instance as "terminating".
	if activeJob != nil && activeJob.Kind == "destroy" {
		terminating := map[string]bool{}
		for _, n := range activeJob.Names {
			terminating[n] = true
		}
		for i := range instances {
			if terminating[instances[i].Name] {
				instances[i].Status = "terminating"
			}
		}
	}

	sandboxInstances, workspaceInstances := splitInstances(instances)
	sandboxTemplates, workspaceTemplates := splitTemplates()
	networkModes := networkModeOptions("sandbox")

	_ = tmpl.ExecuteTemplate(w, "index.html", map[string]any{
		"sandboxTemplates":   sandboxTemplates,
		"workspaceTemplates": workspaceTemplates,
		"instanceTypes":      cfg.AllowedInstanceTypes,
		"networkModes":       networkModes,
		"defaults":           cfg.Defaults,
		"sandboxInstances":   sandboxInstances,
		"workspaceInstances": workspaceInstances,
		"currentView":        currentView,
		"job":                activeJob,
	})
}

// ── Launch ─────────────────────────────────────────────────────────────────

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

	if !isAllowedTemplate(form.TemplateVMID) {
		http.Error(w, "template not allowed", 400)
		return
	}
	if !isAllowedInstanceType(form.InstanceType) {
		http.Error(w, "instance type not allowed", 400)
		return
	}
	if form.NetworkMode == "" {
		form.NetworkMode = cfg.Defaults.NetworkMode
	}
	if form.Count < 1 || form.Count > 10 {
		http.Error(w, "count must be 1..10", 400)
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
	runLaunchJob(w, r, form, "launch", "")
}

func handleSubmitURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}

	form, err := parseURLSubmissionForm(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !isAllowedTemplate(form.TemplateVMID) {
		http.Error(w, "template not allowed", 400)
		return
	}
	if !isAllowedInstanceType(form.InstanceType) {
		http.Error(w, "instance type not allowed", 400)
		return
	}
	if strings.TrimSpace(form.NamePrefix) == "" {
		http.Error(w, "name_prefix required", 400)
		return
	}
	if form.VMIDStart < 100 || form.VMIDStart > 999999 {
		http.Error(w, "vmid_start out of range", 400)
		return
	}

	launch := LaunchForm{
		TemplateVMID: form.TemplateVMID,
		InstanceType: form.InstanceType,
		Count:        1,
		NamePrefix:   form.NamePrefix,
		VMIDStart:    form.VMIDStart,
		Ephemeral:    true,
		NetworkMode:  form.NetworkMode,
	}

	runLaunchJob(w, r, launch, "submit-url", form.URL)
}

func runLaunchJob(w http.ResponseWriter, r *http.Request, form LaunchForm, kind, submissionURL string) {
	view := viewFromRequest(r.FormValue("view"))
	modeSpec, modeLabel, err := validatedNetworkMode(form, view)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if form.NetworkMode == "" {
		form.NetworkMode = cfg.Defaults.NetworkMode
	}

	if !applyLock.TryLock() {
		http.Error(w, "another operation is in progress; try again shortly", 409)
		return
	}

	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := tf.DefaultTimeoutCtx()

	if err := runner.Init(ctx); err != nil {
		applyLock.Unlock()
		cancel()
		http.Error(w, "terraform init failed: "+err.Error(), 500)
		return
	}

	existingVMs := map[string]VMEntry{}
	if show, err := runner.ShowJSON(ctx); err == nil {
		existingVMs = extractVMs(show, loadMetadata())
	}

	newVMs := map[string]VMEntry{}
	names := make([]string, form.Count)
	for i := 0; i < form.Count; i++ {
		name := fmt.Sprintf("%s-%02d", form.NamePrefix, i+1)
		newVMs[name] = VMEntry{
			VMID:         form.VMIDStart + i,
			TemplateVMID: form.TemplateVMID,
			InstanceType: form.InstanceType,
			Bridge:       modeSpec.Bridge,
		}
		names[i] = name
	}

	vms, err := mergeVMs(existingVMs, newVMs)
	if err != nil {
		applyLock.Unlock()
		cancel()
		http.Error(w, err.Error(), 409)
		return
	}

	varPayload := map[string]any{
		"vms":           vms,
		"pve_endpoint":  pveEndpoint,
		"pve_api_token": pveAPIToken,
		"ssh_node_key":  sshNodeKey,
	}

	varFile, err := tf.WriteVarFileJSON(cfg.TerraformDir, varPayload)
	if err != nil {
		applyLock.Unlock()
		cancel()
		http.Error(w, "write var file: "+err.Error(), 500)
		return
	}

	job := newJob(kind, names)
	job.View = view
	job.OS = templateOSLabel(form.TemplateVMID)
	job.Network = modeLabel
	job.SubmissionURL = submissionURL

	go func() {
		defer applyLock.Unlock()
		defer cancel()
		logs, applyErr := runner.Apply(ctx, varFile)
		logs = stripANSI(logs)

		if applyErr == nil {
			metadataMu.Lock()
			meta := loadMetadata()
			for _, n := range names {
				meta[n] = VMMetadata{
					TemplateVMID: form.TemplateVMID,
					InstanceType: form.InstanceType,
					Bridge:       modeSpec.Bridge,
					NetworkMode:  form.NetworkMode,
				}
			}
			saveMetadata(meta)
			metadataMu.Unlock()

			if err := applyPostLaunchNetworkMode(ctx, form, names, newVMs); err != nil {
				applyErr = fmt.Errorf("post-launch network config: %w", err)
			}
			if applyErr == nil && submissionURL != "" {
				stagedPath, err := stageURLSubmission(ctx, names[0], form.TemplateVMID, newVMs[names[0]].VMID, submissionURL)
				if err != nil {
					applyErr = fmt.Errorf("post-launch url staging: %w", err)
				} else {
					job.mu.Lock()
					job.StagedPath = stagedPath
					job.mu.Unlock()
				}
			}

			// Register ephemeral VMs before releasing the lock.
			if form.Ephemeral {
				ephemeralMu.Lock()
				eph := loadEphemeral()
				for _, n := range names {
					eph[n] = true
				}
				saveEphemeral(eph)
				ephemeralMu.Unlock()
			}
		}

		job.complete(logs, applyErr)
	}()

	http.Redirect(w, r, viewURL(view, job.ID), http.StatusSeeOther)
}

// ── Destroy ────────────────────────────────────────────────────────────────

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
	if name == "" || confirm != name {
		http.Error(w, "name confirmation mismatch", 400)
		return
	}

	if !applyLock.TryLock() {
		http.Error(w, "another operation is in progress; try again shortly", 409)
		return
	}
	view := viewFromRequest(r.FormValue("current_view"))

	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := tf.DefaultTimeoutCtx()

	existingVMs := map[string]VMEntry{}
	if show, err := runner.ShowJSON(ctx); err == nil {
		existingVMs = extractVMs(show, loadMetadata())
	}

	varPayload := map[string]any{
		"vms":           existingVMs,
		"pve_endpoint":  pveEndpoint,
		"pve_api_token": pveAPIToken,
		"ssh_node_key":  sshNodeKey,
	}
	varFile, err := tf.WriteVarFileJSON(cfg.TerraformDir, varPayload)
	if err != nil {
		applyLock.Unlock()
		cancel()
		http.Error(w, "write var file: "+err.Error(), 500)
		return
	}

	target := fmt.Sprintf("proxmox_virtual_environment_vm.vm[%q]", name)
	job := newJob("destroy", []string{name})
	job.View = view

	go func() {
		defer applyLock.Unlock()
		defer cancel()
		logs, destroyErr := runner.Destroy(ctx, target, varFile)
		logs = stripANSI(logs)

		// Remove from ephemeral list regardless of outcome.
		ephemeralMu.Lock()
		eph := loadEphemeral()
		delete(eph, name)
		saveEphemeral(eph)
		ephemeralMu.Unlock()

		if destroyErr == nil {
			metadataMu.Lock()
			meta := loadMetadata()
			delete(meta, name)
			saveMetadata(meta)
			metadataMu.Unlock()
		}

		job.complete(logs, destroyErr)
	}()

	http.Redirect(w, r, viewURL(view, job.ID), http.StatusSeeOther)
}

// ── Start / Stop ───────────────────────────────────────────────────────────

func handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	view := viewFromRequest(r.FormValue("current_view"))
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	entry, err := lookupVM(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if err := pveClient.StartVM(entry.Node, entry.VMID); err != nil {
		http.Error(w, "start failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, viewURL(view, ""), http.StatusSeeOther)
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	view := viewFromRequest(r.FormValue("current_view"))
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	entry, err := lookupVM(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if err := pveClient.StopVM(entry.Node, entry.VMID); err != nil {
		http.Error(w, "stop failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, viewURL(view, ""), http.StatusSeeOther)
}

// ── Snapshot / Revert ─────────────────────────────────────────────────────

func handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	snapName := strings.TrimSpace(r.FormValue("snap_name"))
	desc := strings.TrimSpace(r.FormValue("description"))
	view := viewFromRequest(r.FormValue("current_view"))
	if name == "" || snapName == "" {
		http.Error(w, "name and snap_name required", 400)
		return
	}
	entry, err := lookupVM(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if err := pveClient.CreateSnapshot(entry.Node, entry.VMID, snapName, desc); err != nil {
		http.Error(w, "snapshot failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, viewURL(view, ""), http.StatusSeeOther)
}

func handleRevert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	snapName := strings.TrimSpace(r.FormValue("snap_name"))
	view := viewFromRequest(r.FormValue("current_view"))
	if name == "" || snapName == "" {
		http.Error(w, "name and snap_name required", 400)
		return
	}
	entry, err := lookupVM(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if err := pveClient.RevertSnapshot(entry.Node, entry.VMID, snapName); err != nil {
		http.Error(w, "revert failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, viewURL(view, ""), http.StatusSeeOther)
}

// handleListSnapshots returns snapshot list as JSON for a VM.
// GET /snapshots/{name}
func handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/snapshots/")
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	entry, err := lookupVM(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	snaps, err := pveClient.ListSnapshots(entry.Node, entry.VMID)
	if err != nil {
		http.Error(w, "list snapshots: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snaps)
}

// ── Job progress page ──────────────────────────────────────────────────────

func handleJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/job/")
	job := getJob(id)
	if job == nil {
		http.Error(w, "job not found", 404)
		return
	}
	status, logs, errMsg := job.snapshot()
	job.mu.Lock()
	submissionURL := job.SubmissionURL
	stagedPath := job.StagedPath
	job.mu.Unlock()
	_ = tmpl.ExecuteTemplate(w, "job.html", map[string]any{
		"ID":            job.ID,
		"Kind":          job.Kind,
		"Names":         job.Names,
		"Status":        status,
		"Logs":          logs,
		"Error":         errMsg,
		"SubmissionURL": submissionURL,
		"StagedPath":    stagedPath,
	})
}

// ── Console (noVNC) ────────────────────────────────────────────────────────

func handleConsole(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/console/")
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	entry, err := lookupVM(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	vnc, err := pveClient.VNCProxy(entry.Node, entry.VMID)
	if err != nil {
		log.Printf("console %s: vncproxy error: %v", name, err)
		http.Error(w, "vncproxy: "+err.Error(), 502)
		return
	}
	log.Printf("console %s: vncproxy ok, port=%s", name, vnc.Port)
	// Marshal ticket as a JSON string literal so html/template doesn't re-escape it.
	// Using | js in the template double-escapes (= → \u003D → literal \u003D in JS).
	ticketJSON, _ := json.Marshal(vnc.Ticket)
	_ = tmpl.ExecuteTemplate(w, "console.html", map[string]any{
		"Name":   name,
		"Ticket": template.JS(ticketJSON),
		"Port":   vnc.Port,
	})
}

// ── WebSocket VNC Proxy ───────────────────────────────────────────────────

func handleWS(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ws/")
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", 400)
		return
	}

	entry, err := lookupVM(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}

	// Ticket and port are passed as query params by console.html to avoid a
	// second VNCProxy call (which would generate a different ticket).
	ticket := r.URL.Query().Get("ticket")
	vncPort := r.URL.Query().Get("port")
	if ticket == "" || vncPort == "" {
		http.Error(w, "ticket and port required", 400)
		return
	}

	// Connect to Proxmox HTTPS API on port 8006.
	u, _ := url.Parse(pveEndpoint)
	targetAddr := u.Host // e.g. "10.0.0.11:8006"
	if u.Port() == "" {
		targetAddr = u.Hostname() + ":8006"
	}
	log.Printf("ws %s: connecting to %s", name, targetAddr)
	upstream, err := tls.Dial("tcp", targetAddr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		log.Printf("ws %s: upstream connect error: %v", name, err)
		http.Error(w, "upstream connect: "+err.Error(), 502)
		return
	}

	// Upgrade our connection to Proxmox via the vncwebsocket path.
	vncPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/vncwebsocket?port=%s&vncticket=%s",
		entry.Node, entry.VMID, vncPort, url.QueryEscape(ticket))
	log.Printf("ws %s: upgrading to %s", name, vncPath)
	nonce := make([]byte, 16)
	rand.Read(nonce)
	wsKey := base64.StdEncoding.EncodeToString(nonce)
	fmt.Fprintf(upstream,
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n"+
			"Authorization: PVEAPIToken=%s\r\n\r\n",
		vncPath, targetAddr, wsKey, pveAPIToken)

	upstreamBuf := bufio.NewReader(upstream)
	if err := consumeHTTPHeaders(upstreamBuf); err != nil {
		upstream.Close()
		log.Printf("ws %s: upstream handshake error: %v", name, err)
		http.Error(w, "upstream 101: "+err.Error(), 502)
		return
	}
	log.Printf("ws %s: upstream handshake ok, starting relay", name)

	// Complete WebSocket handshake with the browser.
	clientKey := r.Header.Get("Sec-WebSocket-Key")
	h := w.Header()
	h.Set("Upgrade", "websocket")
	h.Set("Connection", "Upgrade")
	h.Set("Sec-WebSocket-Accept", wsAcceptKey(clientKey))
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		h.Set("Sec-WebSocket-Protocol", proto)
	}
	w.WriteHeader(http.StatusSwitchingProtocols)

	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	defer clientConn.Close()
	defer upstream.Close()

	// Flush the buffered 101 response to the client before relaying.
	if err := clientBuf.Flush(); err != nil {
		log.Printf("ws %s: flush 101 error: %v", name, err)
		return
	}

	// Bidirectional relay.
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, clientBuf); done <- struct{}{} }()
	go func() { io.Copy(clientConn, upstreamBuf); done <- struct{}{} }()
	<-done
	log.Printf("ws %s: relay done", name)
}

func consumeHTTPHeaders(r *bufio.Reader) error {
	first := true
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		if first {
			// Expect "HTTP/1.1 101 ..."
			if !strings.HasPrefix(line, "HTTP/") || !strings.Contains(line, " 101 ") {
				return fmt.Errorf("expected 101, got: %s", strings.TrimSpace(line))
			}
			first = false
		}
		if line == "\r\n" {
			return nil
		}
	}
}

func wsAcceptKey(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

// ── Ephemeral Watcher ─────────────────────────────────────────────────────

func watchEphemeral() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		destroyStopped()
	}
}

func destroyStopped() {
	ephemeralMu.Lock()
	eph := loadEphemeral()
	ephemeralMu.Unlock()
	metadata := loadMetadata()

	if len(eph) == 0 {
		return
	}

	// Get current state.
	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	show, err := runner.ShowJSON(ctx)
	if err != nil {
		log.Printf("ephemeral watcher: show failed: %v", err)
		return
	}
	vms := parseInstances(show)

	for _, inst := range vms {
		if !eph[inst.Name] {
			continue
		}
		st, err := pveClient.VMStatus(inst.Node, inst.VMID)
		if err != nil || st != "stopped" {
			// VM is running (or unreachable) — clear any stopped-since entry.
			stoppedSinceMu.Lock()
			delete(stoppedSince, inst.Name)
			stoppedSinceMu.Unlock()
			continue
		}
		// VM is stopped. Record when it was first seen stopped, then wait for
		// the grace period before destroying (allows Windows to reboot).
		stoppedSinceMu.Lock()
		if _, seen := stoppedSince[inst.Name]; !seen {
			stoppedSince[inst.Name] = time.Now()
			stoppedSinceMu.Unlock()
			log.Printf("ephemeral watcher: %s stopped, waiting %v grace period before destroy", inst.Name, ephemeralGrace)
			continue
		}
		since := stoppedSince[inst.Name]
		stoppedSinceMu.Unlock()
		if time.Since(since) < ephemeralGrace {
			continue
		}
		// VM has been stopped for the full grace period — destroy it.
		log.Printf("ephemeral watcher: destroying stopped VM %s (VMID %d)", inst.Name, inst.VMID)
		if !applyLock.TryLock() {
			log.Printf("ephemeral watcher: apply lock held, skipping %s until next tick", inst.Name)
			continue
		}
		go func(name string) {
			defer applyLock.Unlock()
			dctx, dcancel := tf.DefaultTimeoutCtx()
			defer dcancel()

			existingVMs := extractVMs(show, metadata)
			varPayload := map[string]any{
				"vms":           existingVMs,
				"pve_endpoint":  pveEndpoint,
				"pve_api_token": pveAPIToken,
				"ssh_node_key":  sshNodeKey,
			}
			varFile, err := tf.WriteVarFileJSON(cfg.TerraformDir, varPayload)
			if err != nil {
				log.Printf("ephemeral watcher: write var file: %v", err)
				return
			}
			target := fmt.Sprintf("proxmox_virtual_environment_vm.vm[%q]", name)
			if _, err := runner.Destroy(dctx, target, varFile); err != nil {
				log.Printf("ephemeral watcher: destroy %s: %v", name, err)
				return
			}
			ephemeralMu.Lock()
			eph := loadEphemeral()
			delete(eph, name)
			saveEphemeral(eph)
			ephemeralMu.Unlock()

			metadataMu.Lock()
			meta := loadMetadata()
			delete(meta, name)
			saveMetadata(meta)
			metadataMu.Unlock()

			stoppedSinceMu.Lock()
			delete(stoppedSince, name)
			stoppedSinceMu.Unlock()
			log.Printf("ephemeral watcher: destroyed %s", name)
		}(inst.Name)
	}
}

// ── Ephemeral state file ───────────────────────────────────────────────────

func loadEphemeral() map[string]bool {
	b, err := os.ReadFile("ephemeral.json")
	if err != nil {
		return map[string]bool{}
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return map[string]bool{}
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func saveEphemeral(eph map[string]bool) {
	names := make([]string, 0, len(eph))
	for n := range eph {
		names = append(names, n)
	}
	b, _ := json.MarshalIndent(names, "", "  ")
	os.WriteFile("ephemeral.json", b, 0o600)
}

// ── Per-VM metadata ────────────────────────────────────────────────────────

type VMMetadata struct {
	TemplateVMID int    `json:"template_vmid"`
	InstanceType string `json:"instance_type"`
	Bridge       string `json:"bridge"`
	NetworkMode  string `json:"network_mode"`
}

func loadMetadata() map[string]VMMetadata {
	b, err := os.ReadFile(metadataPath())
	if err != nil {
		return map[string]VMMetadata{}
	}
	var meta map[string]VMMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		return map[string]VMMetadata{}
	}
	if meta == nil {
		return map[string]VMMetadata{}
	}
	return meta
}

func saveMetadata(meta map[string]VMMetadata) {
	b, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(metadataPath(), b, 0o600)
}

func networkModeSpec(name string) (*NetworkModeConfig, string, error) {
	if name == "" {
		name = cfg.Defaults.NetworkMode
	}
	spec, ok := cfg.NetworkModes[name]
	if !ok {
		return nil, "", fmt.Errorf("network mode %q not allowed", name)
	}
	label := spec.Label
	if label == "" {
		label = name
	}
	return &spec, label, nil
}

func networkModeLabel(name string) string {
	_, label, err := networkModeSpec(name)
	if err != nil {
		return name
	}
	return label
}

func applyPostLaunchNetworkMode(ctx context.Context, form LaunchForm, names []string, created map[string]VMEntry) error {
	mode, _, err := networkModeSpec(form.NetworkMode)
	if err != nil {
		return err
	}
	if mode.DNSServer == "" {
		return nil
	}

	for _, name := range names {
		entry, ok := created[name]
		if !ok {
			continue
		}
		if err := waitForVMRunning(ctx, entry.VMID, postLaunchVMRunningTimeout); err != nil {
			return fmt.Errorf("%s: wait for vm running: %w", name, err)
		}
		if err := waitForGuestNetworkReady(ctx, entry.VMID, postLaunchGuestReadyTimeout); err != nil {
			return fmt.Errorf("%s: wait for guest agent/network: %w", name, err)
		}
		if err := configureGuestDNSServerWithRetry(ctx, entry.VMID, form.TemplateVMID, mode.DNSServer, postLaunchDNSConfigTimeout); err != nil {
			return fmt.Errorf("%s: configure dns server %s: %w", name, mode.DNSServer, err)
		}
	}

	return nil
}

func waitForVMRunning(ctx context.Context, vmid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out after %v (last status: %v)", timeout, lastErr)
			}
			return fmt.Errorf("timed out after %v", timeout)
		}
		cmdCtx, cancel := context.WithTimeout(ctx, postLaunchCommandTimeout)
		out, err := runNodeCommandOutput(cmdCtx, "qm", "status", strconv.Itoa(vmid))
		cancel()
		if err == nil && vmStatusRunning(out) {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New(strings.TrimSpace(out))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(postLaunchPollInterval):
		}
	}
}

func waitForGuestNetworkReady(ctx context.Context, vmid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out after %v (last error: %v)", timeout, lastErr)
			}
			return fmt.Errorf("timed out after %v", timeout)
		}
		cmdCtx, cancel := context.WithTimeout(ctx, postLaunchCommandTimeout)
		out, err := runNodeCommandOutput(cmdCtx, "qm", "guest", "cmd", strconv.Itoa(vmid), "network-get-interfaces")
		cancel()
		if err == nil {
			ready, parseErr := guestInterfacesReady(out)
			if parseErr == nil && ready {
				return nil
			}
			if parseErr != nil {
				lastErr = parseErr
			} else {
				lastErr = fmt.Errorf("no non-loopback ipv4 interface reported yet")
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(postLaunchPollInterval):
		}
	}
}

func configureGuestDNSServerWithRetry(ctx context.Context, vmid, templateVMID int, dnsServer string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		cmdCtx, cancel := context.WithTimeout(ctx, postLaunchCommandTimeout)
		err := configureGuestDNSServer(cmdCtx, vmid, templateVMID, dnsServer)
		cancel()
		if err == nil {
			verifyCtx, verifyCancel := context.WithTimeout(ctx, postLaunchCommandTimeout)
			err = verifyGuestDNSServer(verifyCtx, vmid, templateVMID, dnsServer)
			verifyCancel()
		}
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v (last error: %v)", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(postLaunchPollInterval):
		}
	}
}

func stageURLSubmission(ctx context.Context, name string, templateVMID, vmid int, submittedURL string) (string, error) {
	switch templateGuestFamily(templateVMID) {
	case "windows":
		stagedPath := `C:\Sandbox\Incoming\submitted-url.txt`
		ps := fmt.Sprintf(`$dir='C:\Sandbox\Incoming'; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Set-Content -Path (Join-Path $dir 'submitted-url.txt') -Value %q`, submittedURL)
		if err := runNodeCommand(ctx, "qm", "guest", "exec", strconv.Itoa(vmid), "--", "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps); err != nil {
			return "", err
		}
		return stagedPath, nil
	default:
		stagedPath := "/home/arvind/submissions/submitted-url.txt"
		py := fmt.Sprintf(`from pathlib import Path
import os
import shlex

url = %q
base = Path("/home/arvind/submissions")
base.mkdir(parents=True, exist_ok=True)
(base / "submitted-url.txt").write_text(url + "\n")
launcher = base / "open-url.sh"
launcher.write_text("#!/bin/sh\nxdg-open " + shlex.quote(url) + "\n")
os.chmod(launcher, 0o755)
`, submittedURL)
		if err := runNodeCommand(ctx, "qm", "guest", "exec", strconv.Itoa(vmid), "--", "python3", "-c", py); err != nil {
			return "", err
		}
		return stagedPath, nil
	}
}

func configureGuestDNSServer(ctx context.Context, vmid, templateVMID int, dnsServer string) error {
	switch templateGuestFamily(templateVMID) {
	case "windows":
		ps := fmt.Sprintf(`$adapter=(Get-NetAdapter | Where-Object Status -eq 'Up' | Select-Object -First 1 -ExpandProperty Name); if (-not $adapter) { throw 'no active adapter found' }; Set-DnsClientServerAddress -InterfaceAlias $adapter -ServerAddresses %s`, dnsServer)
		return runNodeCommand(ctx, "qm", "guest", "exec", strconv.Itoa(vmid), "--", "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	default:
		py := fmt.Sprintf(`from pathlib import Path
import re
import subprocess
import time

dns = %q
p = Path("/etc/dhcpcd.conf")
block = f"# sandbox-dns override start\nnohook resolv.conf\nstatic domain_name_servers={dns}\n# sandbox-dns override end\n"

if p.exists():
    text = p.read_text()
    text = re.sub(r"\n?# sandbox-dns override start\n.*?# sandbox-dns override end\n?", "\n", text, flags=re.S)
    p.write_text(text.rstrip() + "\n\n" + block)

for cmd in (["systemctl", "restart", "dhcpcd"], ["service", "dhcpcd", "restart"], ["dhcpcd", "-n"], ["dhcpcd", "-n", "ens18"]):
    try:
        subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
    except FileNotFoundError:
        pass

time.sleep(2)
Path("/etc/resolv.conf").write_text(f"nameserver {dns}\n")`, dnsServer)
		return runNodeCommand(ctx, "qm", "guest", "exec", strconv.Itoa(vmid), "--", "python3", "-c", py)
	}
}

func verifyGuestDNSServer(ctx context.Context, vmid, templateVMID int, dnsServer string) error {
	switch templateGuestFamily(templateVMID) {
	case "windows":
		ps := fmt.Sprintf(`$adapter=(Get-NetAdapter | Where-Object Status -eq 'Up' | Select-Object -First 1 -ExpandProperty Name); if (-not $adapter) { throw 'no active adapter found' }; $servers=(Get-DnsClientServerAddress -InterfaceAlias $adapter -AddressFamily IPv4).ServerAddresses; if ($servers -notcontains %q) { throw ('dns server not applied: ' + ($servers -join ',')) }`, dnsServer)
		return runNodeCommand(ctx, "qm", "guest", "exec", strconv.Itoa(vmid), "--", "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	default:
		out, err := runNodeCommandOutput(ctx, "qm", "guest", "exec", strconv.Itoa(vmid), "--", "python3", "-c", `from pathlib import Path; print(Path("/etc/resolv.conf").read_text())`)
		if err != nil {
			return err
		}
		if !strings.Contains(out, "nameserver "+dnsServer) {
			return fmt.Errorf("resolver still not set to %s", dnsServer)
		}
		return nil
	}
}

func vmStatusRunning(output string) bool {
	return strings.Contains(output, "status: running")
}

func guestInterfacesReady(output string) (bool, error) {
	var interfaces []struct {
		Name        string `json:"name"`
		IPAddresses []struct {
			IPAddress     string `json:"ip-address"`
			IPAddressType string `json:"ip-address-type"`
		} `json:"ip-addresses"`
	}
	if err := json.Unmarshal([]byte(output), &interfaces); err != nil {
		return false, fmt.Errorf("parse guest interfaces: %w", err)
	}
	for _, iface := range interfaces {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if addr.IPAddressType != "ipv4" {
				continue
			}
			if addr.IPAddress == "" || strings.HasPrefix(addr.IPAddress, "127.") {
				continue
			}
			return true, nil
		}
	}
	return false, nil
}

func templateGuestFamily(vmid int) string {
	for _, t := range cfg.AllowedTemplates {
		if t.VMID != vmid {
			continue
		}
		if strings.HasPrefix(t.Name, "win11-") {
			return "windows"
		}
		return "linux"
	}
	return "linux"
}

func runNodeCommand(ctx context.Context, args ...string) error {
	_, err := runNodeCommandOutput(ctx, args...)
	return err
}

func runNodeCommandOutput(ctx context.Context, args ...string) (string, error) {
	host, err := nodeSSHHost()
	if err != nil {
		return "", err
	}
	if sshNodeKeyPath == "" {
		return "", fmt.Errorf("SSH_NODE_KEY_FILE not configured")
	}

	remote := shellQuote(args...)
	cmdArgs := []string{
		"-F", "/dev/null",
		"-i", sshNodeKeyPath,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "LogLevel=ERROR",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"root@" + host,
		remote,
	}
	cmd := exec.CommandContext(ctx, "ssh", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func nodeSSHHost() (string, error) {
	u, err := url.Parse(pveEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse PVE endpoint: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing host in PVE endpoint")
	}
	return host, nil
}

func shellQuote(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}

func networkModeOptions(view string) []NetworkModeOption {
	order := []string{"offline", "fakenet", "internet"}
	var options []NetworkModeOption
	for _, name := range order {
		spec, label, err := networkModeSpec(name)
		if err != nil {
			continue
		}
		if view == "workspaces" && name != "offline" {
			continue
		}
		options = append(options, NetworkModeOption{
			Name:             name,
			Label:            label,
			Description:      spec.Description,
			Bridge:           spec.Bridge,
			RequireEphemeral: spec.RequireEphemeral,
		})
	}
	return options
}

func validatedNetworkMode(form LaunchForm, view string) (*NetworkModeConfig, string, error) {
	modeName := form.NetworkMode
	if modeName == "" {
		modeName = cfg.Defaults.NetworkMode
	}
	spec, label, err := networkModeSpec(modeName)
	if err != nil {
		return nil, "", err
	}
	if view == "workspaces" && modeName != "offline" {
		return nil, "", fmt.Errorf("workspaces must stay on offline mode")
	}
	if spec.SandboxOnly && view != "sandbox" {
		return nil, "", fmt.Errorf("%s mode is only allowed for sandbox runs", label)
	}
	if spec.RequireEphemeral && !form.Ephemeral {
		return nil, "", fmt.Errorf("%s mode requires ephemeral VMs", label)
	}
	return spec, label, nil
}

func normalizedMetadata(meta VMMetadata) VMMetadata {
	if meta.TemplateVMID == 0 && len(cfg.AllowedTemplates) > 0 {
		meta.TemplateVMID = cfg.AllowedTemplates[0].VMID
	}
	if meta.InstanceType == "" {
		meta.InstanceType = cfg.Defaults.InstanceType
	}
	if meta.NetworkMode == "" {
		meta.NetworkMode = cfg.Defaults.NetworkMode
	}
	mode, _, err := networkModeSpec(meta.NetworkMode)
	if err != nil {
		meta.NetworkMode = cfg.Defaults.NetworkMode
		mode, _, _ = networkModeSpec(meta.NetworkMode)
	}
	if meta.Bridge == "" {
		if mode != nil && mode.Bridge != "" {
			meta.Bridge = mode.Bridge
		} else {
			meta.Bridge = cfg.Defaults.Bridge
		}
	}
	return meta
}

func templateOSLabel(vmid int) string {
	for _, t := range cfg.AllowedTemplates {
		if t.VMID != vmid {
			continue
		}
		switch t.Name {
		case "debian13-sandbox":
			return "Debian 13"
		case "remnux":
			return "REMnux"
		case "win11-sandbox":
			return "Windows 11"
		case "win11-flare":
			return "Windows 11 + FlareVM"
		default:
			return t.Name
		}
	}
	return ""
}

func templateView(vmid int) string {
	for _, t := range cfg.AllowedTemplates {
		if t.VMID != vmid {
			continue
		}
		switch t.Name {
		case "remnux", "win11-flare":
			return "workspaces"
		default:
			return "sandbox"
		}
	}
	return "sandbox"
}

func templateDescription(vmid int) string {
	for _, t := range cfg.AllowedTemplates {
		if t.VMID != vmid {
			continue
		}
		switch t.Name {
		case "debian13-sandbox":
			return "Ephemeral Linux detonation VM"
		case "win11-sandbox":
			return "Ephemeral Windows 11 sandbox"
		case "remnux":
			return "Persistent Linux reverse engineering workspace"
		case "win11-flare":
			return "Persistent Windows 11 FlareVM workspace"
		default:
			return t.Name
		}
	}
	return ""
}

func splitTemplates() (sandboxTemplates, workspaceTemplates []LaunchTemplate) {
	for _, t := range cfg.AllowedTemplates {
		item := LaunchTemplate{
			Name:        t.Name,
			Label:       templateOSLabel(t.VMID),
			Description: templateDescription(t.VMID),
			VMID:        t.VMID,
		}
		if templateView(t.VMID) == "workspaces" {
			workspaceTemplates = append(workspaceTemplates, item)
		} else {
			sandboxTemplates = append(sandboxTemplates, item)
		}
	}
	return sandboxTemplates, workspaceTemplates
}

func splitInstances(instances []Instance) (sandboxInstances, workspaceInstances []Instance) {
	for _, inst := range instances {
		if inst.View == "workspaces" {
			workspaceInstances = append(workspaceInstances, inst)
		} else {
			sandboxInstances = append(sandboxInstances, inst)
		}
	}
	return sandboxInstances, workspaceInstances
}

func metadataPath() string {
	return filepath.Join(cfg.TerraformDir, "metadata.json")
}

func viewFromRequest(view string) string {
	if view == "workspaces" {
		return "workspaces"
	}
	return "sandbox"
}

func viewURL(view, jobID string) string {
	view = viewFromRequest(view)
	if jobID != "" {
		return "/?view=" + view + "&job=" + jobID
	}
	return "/?view=" + view
}

// ── Terraform state helpers ───────────────────────────────────────────────

func extractVMs(show map[string]any, metadata map[string]VMMetadata) map[string]VMEntry {
	result := map[string]VMEntry{}
	resources := showResources(show)
	for _, r := range resources {
		vals, _ := r["values"].(map[string]any)
		name, _ := vals["name"].(string)
		vmidF, _ := vals["vm_id"].(float64)
		if name != "" {
			meta := normalizedMetadata(metadata[name])
			result[name] = VMEntry{
				VMID:         int(vmidF),
				TemplateVMID: meta.TemplateVMID,
				InstanceType: meta.InstanceType,
				Bridge:       meta.Bridge,
			}
		}
	}
	return result
}

func parseInstances(show map[string]any) []Instance {
	resources := showResources(show)
	var result []Instance
	for _, r := range resources {
		vals, _ := r["values"].(map[string]any)
		name, _ := vals["name"].(string)
		vmidF, _ := vals["vm_id"].(float64)
		node, _ := vals["node_name"].(string)
		if name == "" {
			continue
		}
		result = append(result, Instance{
			Name: name,
			VMID: int(vmidF),
			Node: node,
		})
	}
	return result
}

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

func mergeVMs(existing, incoming map[string]VMEntry) (map[string]VMEntry, error) {
	for name, entry := range incoming {
		if _, exists := existing[name]; exists {
			return nil, fmt.Errorf("VM name %q already exists", name)
		}
		for eName, eEntry := range existing {
			if eEntry.VMID == entry.VMID {
				return nil, fmt.Errorf("VMID %d already used by %q", entry.VMID, eName)
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

func lookupVM(name string) (Instance, error) {
	runner := tf.Runner{Dir: cfg.TerraformDir}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	show, err := runner.ShowJSON(ctx)
	if err != nil {
		return Instance{}, fmt.Errorf("read state: %w", err)
	}
	for _, inst := range parseInstances(show) {
		if inst.Name == name {
			return inst, nil
		}
	}
	return Instance{}, fmt.Errorf("VM %q not found in state", name)
}

// ── Form parsing + allowlist ──────────────────────────────────────────────

func parseLaunchForm(r *http.Request) (LaunchForm, error) {
	tpl, err := parseInt(r.FormValue("template_vmid"))
	if err != nil {
		return LaunchForm{}, fmt.Errorf("invalid template_vmid: %w", err)
	}
	count, err := parseInt(r.FormValue("count"))
	if err != nil {
		return LaunchForm{}, fmt.Errorf("invalid count: %w", err)
	}
	vmidStart, err := parseInt(r.FormValue("vmid_start"))
	if err != nil {
		return LaunchForm{}, fmt.Errorf("invalid vmid_start: %w", err)
	}
	return LaunchForm{
		TemplateVMID: tpl,
		InstanceType: r.FormValue("instance_type"),
		Count:        count,
		NamePrefix:   r.FormValue("name_prefix"),
		VMIDStart:    vmidStart,
		Ephemeral:    r.FormValue("ephemeral") == "on",
		NetworkMode:  strings.TrimSpace(r.FormValue("network_mode")),
	}, nil
}

func parseURLSubmissionForm(r *http.Request) (URLSubmissionForm, error) {
	tpl, err := parseInt(r.FormValue("template_vmid"))
	if err != nil {
		return URLSubmissionForm{}, fmt.Errorf("invalid template_vmid: %w", err)
	}
	vmidStart, err := parseInt(r.FormValue("vmid_start"))
	if err != nil {
		return URLSubmissionForm{}, fmt.Errorf("invalid vmid_start: %w", err)
	}
	rawURL := strings.TrimSpace(r.FormValue("submission_url"))
	if rawURL == "" {
		return URLSubmissionForm{}, fmt.Errorf("submission_url required")
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return URLSubmissionForm{}, fmt.Errorf("invalid submission_url: %w", err)
	}
	return URLSubmissionForm{
		URL:          rawURL,
		TemplateVMID: tpl,
		InstanceType: r.FormValue("instance_type"),
		NamePrefix:   r.FormValue("name_prefix"),
		VMIDStart:    vmidStart,
		NetworkMode:  strings.TrimSpace(r.FormValue("network_mode")),
	}, nil
}

func parseInt(s string) (int, error) {
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
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

// ── Utilities ─────────────────────────────────────────────────────────────

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// extractHostname returns just the host portion of an endpoint URL.
// "https://10.0.0.11:8006" → "10.0.0.11"
// "https://pve.example.com" → "pve.example.com"
func extractHostname(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		// Fallback: strip scheme manually.
		s := strings.TrimPrefix(endpoint, "https://")
		s = strings.TrimPrefix(s, "http://")
		if i := strings.IndexAny(s, "/:"); i > 0 {
			s = s[:i]
		}
		return s
	}
	return u.Hostname()
}
