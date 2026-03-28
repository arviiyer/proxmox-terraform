provider "proxmox" {
  endpoint  = var.pve_endpoint
  api_token = var.pve_api_token
  insecure  = true

  ssh {
    agent       = false
    username    = "root"
    private_key = var.ssh_node_key
  }
}

locals {
  flavors = {
    # Linux analysis VMs (remnux, debian13-sandbox)
    "sandbox-small"  = { cores = 2, memory = 4096 }
    # Windows analysis VMs (win11-sandbox)
    "sandbox-medium" = { cores = 4, memory = 8192 }
    # RE workstation (win11-flare), heavy tooling
    "sandbox-large"  = { cores = 6, memory = 16384 }
  }
  f = lookup(local.flavors, var.instance_type, local.flavors["sandbox-medium"])
}

resource "proxmox_virtual_environment_vm" "vm" {
  for_each = var.vms

  name      = each.key
  node_name = "summerset"
  vm_id     = each.value.vmid

  stop_on_destroy = true

  clone {
    vm_id     = var.template_vmid
    node_name = "summerset"
    full      = true
  }

  cpu {
    cores = local.f.cores
  }

  memory {
    dedicated = local.f.memory
  }

  network_device {
    bridge = var.bridge
    model  = "virtio"
  }

  # Don't resize disks (sandbox templates have appropriate sizes).
  # Don't touch cloud-init (not used for sandbox VMs).
  lifecycle {
    ignore_changes = [disk, initialization]
  }
}
