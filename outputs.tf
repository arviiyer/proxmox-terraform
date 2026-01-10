output "instances" {
  value = [
    for v in proxmox_virtual_environment_vm.vm : {
      vm_id = v.vm_id
      name  = v.name
      node  = v.node_name

      private_ip = try(
        one(flatten([
          for iface_ips in v.ipv4_addresses : [
            for ip in iface_ips : ip
            if can(regex("^10\\.", ip))
          ]
        ])),
        null
      )
    }
  ]
}
