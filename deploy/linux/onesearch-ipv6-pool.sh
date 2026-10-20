#!/usr/bin/env bash
# Expand this host's routed IPv6 prefix into a source-address pool.
#
# The provider routes the whole prefix it hands to this host (verified by
# egress probes from several subnets), so any address inside the prefix can
# be used as the source of an outbound request. Listing them on the interface
# is what makes them visible to provider.hostPublicIPs(), which is what the
# "auto" source_ip_pool enumerates: each distinct source address is a
# distinct client as far as upstream rate limiting and bot detection care.
#
# One address per /64 on purpose: upstreams that aggregate by prefix rate
# limit a whole /64 as a single client, so 256 addresses inside one /64 buy
# nothing while 256 addresses spread over 256 /64s look like 256 clients.
# A /56 therefore yields exactly 256 usable source addresses.
#
# The addresses configured in netplan belong to the host itself and are never
# removed: the pool is built around them and --clear leaves them alone.
#
# Usage:
#   onesearch-ipv6-pool.sh          add the pool
#   onesearch-ipv6-pool.sh --clear  remove the pool again
set -u

IFACE=${IFACE:-eth0}
COUNT=${COUNT:-256}
STATE=/run/onesearch-ipv6-pool.base

add_pool() {
  python3 - "$IFACE" "$COUNT" "$STATE" <<'PY'
import glob, ipaddress, re, subprocess, sys

iface, count, state = sys.argv[1], int(sys.argv[2]), sys.argv[3]


def netplan_addresses():
    """IPv6 addresses the host configuration owns; we never remove them."""
    found = set()
    for path in glob.glob("/etc/netplan/*.yaml") + glob.glob("/etc/netplan/*.yml"):
        try:
            with open(path) as fh:
                text = fh.read()
        except OSError:
            continue
        for raw in re.findall(r"([0-9A-Fa-f:]+/\d{1,3})", text):
            try:
                addr = ipaddress.ip_interface(raw)
            except ValueError:
                continue
            if addr.version == 6:
                found.add(str(addr.ip))
    return found


out = subprocess.run(["ip", "-6", "-o", "addr", "show", "dev", iface, "scope", "global"],
                     capture_output=True, text=True).stdout
ips = [l.split()[3] for l in out.splitlines() if " inet6 " in l]
if not ips:
    print("[onesearch-ipv6-pool] no global IPv6 on %s, nothing to expand" % iface)
    sys.exit(0)

owned = netplan_addresses()
present = {str(ipaddress.ip_interface(i).ip) for i in ips}
host = None
for candidate in ips:
    ip = ipaddress.ip_interface(candidate)
    if str(ip.ip) in owned:
        host = ip
        break
if host is None:
    host = ipaddress.ip_interface(ips[0])

net = ipaddress.ip_network("%s/%d" % (host.ip, host.network.prefixlen), strict=False)
per64 = 2 ** 64
subnets = net.num_addresses // per64
offset = int(host.ip) % per64
if subnets < count:
    count = subnets

added = 0
seen = set()
for i in range(count):
    base = ipaddress.ip_address(int(net.network_address) + i * per64)
    cand = ipaddress.ip_address(int(base) + offset)
    if str(cand) in present or str(cand) in seen:
        continue
    seen.add(str(cand))
    r = subprocess.run(["ip", "-6", "addr", "add",
                        "%s/%d" % (cand, net.prefixlen),
                        "dev", iface, "nodad"],
                       capture_output=True, text=True)
    if r.returncode == 0:
        added += 1
    elif r.stderr.strip():
        print("[onesearch-ipv6-pool] %s: %s" % (cand, r.stderr.strip()))

with open(state, "w") as fh:
    fh.write("%s\n" % host.ip)
print("[onesearch-ipv6-pool] %s: %s, one address in each of %d /64s, added %d"
      % (iface, host.ip, count, added))
PY
}

clear_pool() {
  python3 - "$IFACE" "$STATE" <<'PY'
import glob, ipaddress, os, re, subprocess, sys

iface, state = sys.argv[1], sys.argv[2]


def netplan_addresses():
    found = set()
    for path in glob.glob("/etc/netplan/*.yaml") + glob.glob("/etc/netplan/*.yml"):
        try:
            with open(path) as fh:
                text = fh.read()
        except OSError:
            continue
        for raw in re.findall(r"([0-9A-Fa-f:]+/\d{1,3})", text):
            try:
                addr = ipaddress.ip_interface(raw)
            except ValueError:
                continue
            if addr.version == 6:
                found.add(str(addr.ip))
    return found


protected = netplan_addresses()
if os.path.exists(state):
    with open(state) as fh:
        protected.add(fh.read().strip())

out = subprocess.run(["ip", "-6", "-o", "addr", "show", "dev", iface, "scope", "global"],
                     capture_output=True, text=True).stdout
removed = 0
for line in out.splitlines():
    if " inet6 " not in line:
        continue
    addr = line.split()[3].split("/")[0]
    if addr in protected:
        continue
    r = subprocess.run(["ip", "-6", "addr", "del", line.split()[3], "dev", iface],
                       capture_output=True, text=True)
    if r.returncode == 0:
        removed += 1
print("[onesearch-ipv6-pool] %s: removed %d source addresses, kept %s"
      % (iface, removed, ",".join(sorted(protected)) or "nothing"))
PY
}

case "${1:-}" in
  --clear) clear_pool ;;
  *)       add_pool ;;
esac
