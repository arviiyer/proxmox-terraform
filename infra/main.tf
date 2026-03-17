provider "proxmox" {
  endpoint  = var.pve_endpoint
  api_token = var.pve_api_token
  insecure  = true
}

locals {
  flavors = {
    "general-nano"   = { cores = 1, memory = 1024, disk = 20 }
    "general-small"  = { cores = 2, memory = 2048, disk = 40 }
    "general-medium" = { cores = 4, memory = 8192, disk = 80 }
    "general-large"  = { cores = 6, memory = 16384, disk = 160 }
    "general-xlarge" = { cores = 8, memory = 24576, disk = 256 }

    "compute-medium" = { cores = 8, memory = 8192, disk = 80 }
    "compute-large"  = { cores = 12, memory = 12288, disk = 160 }
    "compute-xlarge" = { cores = 16, memory = 16384, disk = 256 }

    "memory-medium" = { cores = 4, memory = 16384, disk = 80 }
    "memory-large"  = { cores = 6, memory = 24576, disk = 160 }
    "memory-xlarge" = { cores = 8, memory = 32768, disk = 256 }
  }

  f = lookup(local.flavors, var.instance_type, local.flavors["general-small"])
}

resource "proxmox_virtual_environment_vm" "vm" {

  for_each = var.vms

  name      = each.key
  node_name = each.value.node
  vm_id     = each.value.vmid

  stop_on_destroy = true

  clone {
    vm_id     = var.template_vmid
    node_name = var.template_node
    full      = var.full_clone
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

  disk {
    datastore_id = var.ci_datastore
    interface    = "scsi0"
    size         = local.f.disk
  }

  initialization {
    datastore_id      = var.ci_datastore
    user_data_file_id = var.user_data_file_id != "" ? var.user_data_file_id : null

    user_account {
      username = var.ci_user
      keys     = [var.ssh_public_key]
    }

    ip_config {
      ipv4 {
        address = "dhcp"
      }
    }
  }
}
