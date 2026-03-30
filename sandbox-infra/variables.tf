variable "vms" {
  description = "Map of VM names to their immutable launch settings. Node is always summerset."
  type = map(object({
    vmid          = number
    template_vmid = number
    instance_type = string
    bridge        = string
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
  description = "Legacy single-template input. Kept only for compatibility with older var files."
  type        = number
  default     = 8010
}

variable "bridge" {
  description = "Legacy single-bridge input. Kept only for compatibility with older var files."
  type        = string
  default     = "vmbr1"
}

variable "instance_type" {
  description = "Legacy single-size input. Kept only for compatibility with older var files."
  type        = string
  default     = "sandbox-medium"
}
