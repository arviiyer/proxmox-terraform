package proxmox

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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

// UploadSnippet uploads content as a snippets file to the given storage on node.
// Returns the file ID in the format "storage:snippets/filename" ready for use
// as user_data_file_id in Terraform.
func (c *Client) UploadSnippet(node, storage, filename, content string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("content", "snippets"); err != nil {
		return "", err
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(fw, content); err != nil {
		return "", err
	}
	w.Close()

	url := fmt.Sprintf("%s/api2/json/nodes/%s/storage/%s/upload", c.endpoint, node, storage)
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.apiToken)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("proxmox API %s: %s", resp.Status, body)
	}
	return fmt.Sprintf("%s:snippets/%s", storage, filename), nil
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
