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

variable "user_data_file_id" {
  description = "Snippets file ID for cloud-init user data, e.g. proxmox-nas:snippets/user-data.yaml. Empty string means no user data."
  type        = string
  default     = ""
}
