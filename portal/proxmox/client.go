package proxmox

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client makes direct Proxmox API calls for lightweight operations (start/stop/status)
// that don't warrant a full Terraform apply.
type Client struct {
	endpoint string
	apiToken string
	http     *http.Client
}

func New(endpoint, apiToken string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiToken: apiToken,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// VMStatus returns the power state of a VM: "running", "stopped", "paused", etc.
func (c *Client) VMStatus(node string, vmid int) (string, error) {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/status/current", c.endpoint, node, vmid)
	body, err := c.get(url)
	if err != nil {
		return "", err
	}
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse status response: %w", err)
	}
	return resp.Data.Status, nil
}

// StartVM powers on a stopped VM.
func (c *Client) StartVM(node string, vmid int) error {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/status/start", c.endpoint, node, vmid)
	return c.post(url)
}

// StopVM sends a graceful ACPI shutdown with forceStop=1 as fallback,
// matching the behaviour of the Proxmox web UI stop button.
func (c *Client) StopVM(node string, vmid int) error {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/status/shutdown", c.endpoint, node, vmid)
	return c.post(url, "forceStop=1")
}

// SetProtection enables or disables Proxmox's native VM protection flag,
// which prevents the VM from being deleted or modified via the Proxmox UI or API.
func (c *Client) SetProtection(node string, vmid int, protect bool) error {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/config", c.endpoint, node, vmid)
	val := "0"
	if protect {
		val = "1"
	}
	return c.put(url, "protection="+val)
}

// VNCProxyResult holds the ticket and port returned by Proxmox's vncproxy API.
type VNCProxyResult struct {
	Ticket string `json:"ticket"`
	Port   int    `json:"port"`
	Cert   string `json:"cert"`
}

// VNCProxy creates a WebSocket-capable VNC proxy for a VM and returns the
// ticket and port. The caller connects to {node}:{port} with the ticket as
// the VNC password.
func (c *Client) VNCProxy(node string, vmid int) (VNCProxyResult, error) {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/vncproxy", c.endpoint, node, vmid)
	body, err := c.postResponse(url, "websocket=1")
	if err != nil {
		return VNCProxyResult{}, err
	}
	var resp struct {
		Data VNCProxyResult `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return VNCProxyResult{}, fmt.Errorf("parse vncproxy response: %w", err)
	}
	return resp.Data, nil
}

// Snapshot represents a Proxmox VM snapshot.
type Snapshot struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parent      string `json:"parent"`
	SnapTime    int64  `json:"snaptime"`
}

// ListSnapshots returns all snapshots for a VM, excluding the "current" pseudo-snapshot.
func (c *Client) ListSnapshots(node string, vmid int) ([]Snapshot, error) {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/snapshot", c.endpoint, node, vmid)
	body, err := c.get(url)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []Snapshot `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse snapshot list: %w", err)
	}
	var snaps []Snapshot
	for _, s := range resp.Data {
		if s.Name != "current" {
			snaps = append(snaps, s)
		}
	}
	return snaps, nil
}

// CreateSnapshot takes a snapshot of a running or stopped VM.
func (c *Client) CreateSnapshot(node string, vmid int, name, description string) error {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/snapshot", c.endpoint, node, vmid)
	body := fmt.Sprintf("snapname=%s&description=%s",
		strings.ReplaceAll(name, " ", "%20"),
		strings.ReplaceAll(description, " ", "%20"))
	return c.post(url, body)
}

// RevertSnapshot rolls a VM back to a named snapshot.
func (c *Client) RevertSnapshot(node string, vmid int, snapName string) error {
	url := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/snapshot/%s/rollback", c.endpoint, node, vmid, snapName)
	return c.post(url)
}

func (c *Client) put(url string, formBody string) error {
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(formBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.apiToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("proxmox API %s: %s", resp.Status, body)
	}
	return nil
}

func (c *Client) get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.apiToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("proxmox API %s: %s", resp.Status, body)
	}
	return body, nil
}

// postResponse is like post but returns the response body.
func (c *Client) postResponse(url string, formBody string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(formBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.apiToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("proxmox API %s: %s", resp.Status, body)
	}
	return body, nil
}

func (c *Client) post(url string, formBody ...string) error {
	var bodyReader io.Reader
	contentType := ""
	if len(formBody) > 0 && formBody[0] != "" {
		bodyReader = strings.NewReader(formBody[0])
		contentType = "application/x-www-form-urlencoded"
	}
	req, err := http.NewRequest(http.MethodPost, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.apiToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("proxmox API %s: %s", resp.Status, body)
	}
	return nil
}
