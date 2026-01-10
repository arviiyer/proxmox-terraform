terraform {
  required_version = ">= 1.5.0"

  required_providers {
    proxmox = {
      source = "bpg/proxmox"
      # pin to a known-good range; bump later once stable
      version = "~> 0.6"
    }
  }
}

