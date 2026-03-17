variable "vms" {
  description = "Map of VM names to their configuration"
  type = map(object({
    vmid = number
    node = string
  }))
  default = {}
}

variable "pve_endpoint" {
  description = "Proxmox API endpoint, e.g. https://summerset:8006/"
  type        = string
}

variable "pve_api_token" {
  description = "Proxmox API token ID, e.g. root@pam!terraform=UUID"
  type        = string
  sensitive   = true
}


variable "template_vmid" {
  description = "Template VMID (your 'AMI')"
  type        = number
  default     = 8000
}

variable "template_node" {
  description = "Node where the template VM config lives (used for cross-node cloning)"
  type        = string
  default     = "summerset"
}

variable "bridge" {
  type    = string
  default = "vmbr0"
}

variable "name_prefix" {
  type    = string
  default = "vm"
}

variable "instance_count" {
  type    = number
  default = 1
}

variable "vmid_start" {
  description = "Starting VMID for created instances (avoid collisions)"
  type        = number
  default     = 110
}

variable "ci_user" {
  type    = string
  default = "arvind"
}

variable "ssh_public_key" {
  type = string
}

variable "ssh_node_key" {
  description = "Private SSH key content for the provider to connect to Proxmox nodes (needed for snippet file uploads)."
  type        = string
  sensitive   = true
}

variable "instance_type" {
  description = "EC2-like flavor mapping"
  type        = string
  default     = "general-small"
}

variable "ci_datastore" {
  type    = string
  default = "local-lvm"
}

variable "full_clone" {
  description = "true = full clone (copies disk), false = linked clone (fast/thin)"
  type        = bool
  default     = false
}

variable "user_data" {
  description = "Raw cloud-init user data content. Empty string means no user data."
  type        = string
  default     = ""
}

variable "snippets_storage" {
  description = "Proxmox storage ID to upload user data snippets to. Must have snippets content type enabled."
  type        = string
  default     = "proxmox-nas"
}
