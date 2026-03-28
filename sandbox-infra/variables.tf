variable "vms" {
  description = "Map of VM names to their VMID. Node is always summerset."
  type = map(object({
    vmid = number
  }))
  default = {}
}

variable "pve_endpoint" {
  description = "Proxmox API endpoint, e.g. https://10.0.0.11:8006"
  type        = string
}

variable "pve_api_token" {
  description = "Proxmox API token ID, e.g. sandbox@pam!token=UUID"
  type        = string
  sensitive   = true
}

variable "ssh_node_key" {
  description = "Private SSH key content for the provider to SSH into Proxmox nodes."
  type        = string
  sensitive   = true
}

variable "template_vmid" {
  description = "Template VMID to clone from."
  type        = number
  default     = 8010
}

variable "bridge" {
  description = "Network bridge. Use vmbr1 for isolated sandbox, vmbr0 for internet access."
  type        = string
  default     = "vmbr1"
}

variable "instance_type" {
  description = "Sandbox flavor: sandbox-small, sandbox-medium, sandbox-large"
  type        = string
  default     = "sandbox-medium"
}
