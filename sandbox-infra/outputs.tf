output "instances" {
  value = [
    for v in proxmox_virtual_environment_vm.vm : {
      vm_id = v.vm_id
      name  = v.name
      node  = v.node_name
    }
  ]
}
