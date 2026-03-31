#!/bin/sh
set -eu

VMID="${VMID:-8050}"
NAME="${NAME:-sandbox-fakenet}"
TEMPLATE_VMID="${TEMPLATE_VMID:-8010}"
STORAGE="${STORAGE:-local-lvm}"
TEMP_BRIDGE="${TEMP_BRIDGE:-vmbr0}"
FINAL_BRIDGE="${FINAL_BRIDGE:-vmbr1}"
STATIC_IP_CIDR="${STATIC_IP_CIDR:-10.0.2.53/24}"
STATIC_IP="${STATIC_IP_CIDR%/*}"

wait_for_guest_exec() {
    i=0
    while [ "$i" -lt 60 ]; do
        if qm guest exec "$VMID" -- true >/dev/null 2>&1; then
            return 0
        fi
        sleep 5
        i=$((i + 1))
    done
    echo "guest agent did not become ready for VM $VMID" >&2
    return 1
}

wait_for_shutdown() {
    i=0
    while [ "$i" -lt 24 ]; do
        if ! qm status "$VMID" | grep -q "status: running"; then
            return 0
        fi
        sleep 5
        i=$((i + 1))
    done
    echo "VM $VMID did not shut down in time; forcing stop" >&2
    qm stop "$VMID" >/dev/null
}

qm_exists() {
    qm config "$VMID" >/dev/null 2>&1
}

if ! qm_exists; then
    qm clone "$TEMPLATE_VMID" "$VMID" --name "$NAME" --full --storage "$STORAGE"
fi

qm set "$VMID" --onboot 1 >/dev/null
qm set "$VMID" --net0 "virtio,bridge=${TEMP_BRIDGE},firewall=0" >/dev/null
if ! qm status "$VMID" | grep -q "status: running"; then
    qm start "$VMID" >/dev/null
fi

wait_for_guest_exec

qm guest exec "$VMID" -- /bin/sh -lc \
    "export DEBIAN_FRONTEND=noninteractive; dpkg -s inetsim >/dev/null 2>&1 || (apt-get update && apt-get install -y inetsim)"

qm guest exec "$VMID" -- python3 -c "
from pathlib import Path

script = Path('/usr/local/bin/fakenet-dns.py')
script.write_text('''#!/usr/bin/env python3
import socket
import struct

RESP_IP = bytes([10, 0, 2, 53])


def handle_query(data: bytes) -> bytes:
    if len(data) < 12:
        return b''
    txid = data[:2]
    flags = data[2:4]
    qdcount = struct.unpack('>H', data[4:6])[0]
    if qdcount < 1:
        return b''
    offset = 12
    while offset < len(data):
        length = data[offset]
        offset += 1
        if length == 0:
            break
        offset += length
    if offset + 4 > len(data):
        return b''
    question = data[12:offset + 4]
    qtype, qclass = struct.unpack('>HH', data[offset:offset + 4])
    rd = flags[1] & 1
    header = txid + bytes([0x81, 0x80 | rd])
    if qtype == 1 and qclass == 1:
        answer = bytes([0xC0, 0x0C]) + struct.pack('>HHIH', 1, 1, 60, 4) + RESP_IP
        return header + struct.pack('>HHHH', 1, 1, 0, 0) + question + answer
    return header + struct.pack('>HHHH', 1, 0, 0, 0) + question


def main() -> None:
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind(('10.0.2.53', 53))
    while True:
        data, addr = sock.recvfrom(512)
        response = handle_query(data)
        if response:
            sock.sendto(response, addr)


if __name__ == '__main__':
    main()
''')
script.chmod(0o755)
"

qm guest exec "$VMID" -- python3 -c "
from pathlib import Path

service = Path('/etc/systemd/system/fakenet-dns.service')
service.write_text('''[Unit]
Description=FakeNet wildcard DNS responder
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/fakenet-dns.py
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
''')
"

qm guest exec "$VMID" -- python3 -c "
from pathlib import Path

Path('/etc/network/interfaces').write_text('''# This file describes the network interfaces available on your system
# and how to activate them. For more information, see interfaces(5).

source /etc/network/interfaces.d/*

auto lo
iface lo inet loopback

allow-hotplug ens18
iface ens18 inet static
    address ${STATIC_IP_CIDR}
''')
"

qm guest exec "$VMID" -- python3 -c "
from pathlib import Path
import re

path = Path('/etc/inetsim/inetsim.conf')
text = path.read_text()
text = re.sub(r'\\n?service_bind_address\\s+.*', '', text)
text = re.sub(r'\\n?dns_default_ip\\s+.*', '', text)
text = text.rstrip() + '\\nservice_bind_address ${STATIC_IP}\\ndns_default_ip ${STATIC_IP}\\n'
path.write_text(text)
"

qm guest exec "$VMID" -- systemctl disable --now inetsim >/dev/null 2>&1 || true
qm guest exec "$VMID" -- systemctl disable --now fakenet-dns.service >/dev/null 2>&1 || true

qm shutdown "$VMID" >/dev/null || true
wait_for_shutdown

qm set "$VMID" --net0 "virtio,bridge=${FINAL_BRIDGE},firewall=0" >/dev/null
qm start "$VMID" >/dev/null

wait_for_guest_exec

qm guest exec "$VMID" -- systemctl daemon-reload
qm guest exec "$VMID" -- systemctl enable --now fakenet-dns.service
qm guest exec "$VMID" -- systemctl enable --now inetsim
qm guest exec "$VMID" -- systemctl is-active fakenet-dns.service
qm guest exec "$VMID" -- systemctl is-active inetsim
qm guest exec "$VMID" -- ip addr show ens18
