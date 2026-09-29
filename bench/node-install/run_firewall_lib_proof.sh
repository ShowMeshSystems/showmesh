#!/usr/bin/env bash
# Direct-sourcing proof for deploy/install/lib/firewall.sh's non-ufw backends: firewalld, a raw
# nftables ruleset with a drop policy, no firewall active, --firewall's own host table, and
# --no-firewall. The ufw path is proved end to end through the real installer instead
# (run_firewall_ufw_proof.sh), since ufw needs nothing else showmesh-install would exercise.
# Runs inside a throwaway container already given the capabilities and packages its case
# needs; see run_firewall_bench.sh.
set -uo pipefail

CASE="${1:?usage: run_firewall_lib_proof.sh firewalld|nftables-drop|nftables-forward-drop|ssh-port|none|firewall-flag|no-firewall-flag}"
FAILED=0

check() {
  if eval "$2"; then echo "PASS: $1"; else echo "FAIL: $1"; FAILED=$((FAILED + 1)); fi
}

cd /tmp || exit 1
cp -r /repo/deploy/install/lib .
# shellcheck source=/dev/null
. lib/common.sh
# shellcheck source=/dev/null
. lib/firewall.sh
AGENT_ENV=/tmp/agent.env
COORD_ENV=/tmp/coord.env
touch "$AGENT_ENV" "$COORD_ENV"
export DEBIAN_FRONTEND=noninteractive

case "$CASE" in
firewalld)
  apt-get install -y -qq --no-install-recommends firewalld dbus iptables >/dev/null 2>&1
  mkdir -p /run/dbus
  dbus-daemon --system --fork
  /usr/sbin/firewalld --nofork --nopid >/var/log/firewalld.log 2>&1 &
  for _ in $(seq 1 20); do [ "$(firewall-cmd --state 2>/dev/null)" = running ] && break; sleep 1; done
  if [ "$(firewall-cmd --state 2>/dev/null)" != running ]; then
    echo "firewalld did not start in this container; not run"
    exit 0
  fi

  export OPT_FIREWALL=""
  firewall_setup render "" > /tmp/first.log 2>&1
  cat /tmp/first.log
  check "every render port is opened in firewalld" \
    "grep -q 'tcp 80 .*is open in firewalld' /tmp/first.log && grep -q 'udp 5353 .*is open in firewalld' /tmp/first.log && grep -q 'tcp 5959-5999 .*is open in firewalld' /tmp/first.log"
  firewall-cmd --permanent --list-services > /tmp/services.txt
  firewall-cmd --list-ports > /tmp/ports.txt
  check "the showmesh service is in the zone's permanent config" "grep -qw showmesh /tmp/services.txt"
  check "the runtime zone has the ports without a reload" "grep -qw 5959-5999/udp /tmp/ports.txt && grep -qw 80/tcp /tmp/ports.txt"
  check "the service definition names ShowMesh" "grep -q '<short>ShowMesh</short>' /etc/firewalld/services/showmesh.xml"

  firewall_setup render "" > /tmp/second.log 2>&1
  cat /tmp/second.log
  firewall-cmd --permanent --list-services | tr ' ' '\n' > /tmp/services-after.txt
  check "a second run stays idempotent (one showmesh entry)" "[ \"\$(grep -cx showmesh /tmp/services-after.txt)\" = 1 ]"
  ;;

nftables-drop)
  apt-get install -y -qq --no-install-recommends nftables >/dev/null 2>&1
  nft add table inet ownertable
  nft add chain inet ownertable input '{ type filter hook input priority 0; policy drop; }'
  nft list ruleset > /tmp/ruleset-before.txt
  export OPT_FIREWALL=""
  firewall_setup audio "" > /tmp/warn.log 2>&1
  cat /tmp/warn.log
  nft list ruleset > /tmp/ruleset-after.txt
  check "a foreign drop-policy ruleset is left unchanged" "diff -q /tmp/ruleset-before.txt /tmp/ruleset-after.txt >/dev/null"
  check "the warning names the file nftables loads on Debian" "grep -q '/etc/nftables.conf' /tmp/warn.log"
  check "the warning lists this role's rules" "grep -q 'udp dport 32320 accept' /tmp/warn.log && grep -q 'udp dport 5353 accept' /tmp/warn.log"
  ;;

nftables-forward-drop)
  apt-get install -y -qq --no-install-recommends nftables >/dev/null 2>&1
  nft add table inet ownertable
  nft add chain inet ownertable input '{ type filter hook input priority 0; policy accept; }'
  nft add table ip dockerlike
  nft add chain ip dockerlike FORWARD '{ type filter hook forward priority 0; policy drop; }'
  export OPT_FIREWALL=""
  firewall_setup audio "" > /tmp/fwd.log 2>&1
  cat /tmp/fwd.log
  check "an accept-policy input chain plus a drop-policy FORWARD chain is not a drop host" \
    "[ \"\$FIREWALL_KIND\" = none ] && grep -q 'No firewall is active' /tmp/fwd.log"
  ;;

ssh-port)
  apt-get install -y -qq --no-install-recommends nftables netbase >/dev/null 2>&1
  mkdir -p /tmp/fakebin
  printf '#!/bin/sh\nprintf "port 2222\\nport 22022\\nlistenaddress 0.0.0.0\\n"\n' > /tmp/fakebin/sshd
  chmod +x /tmp/fakebin/sshd
  check "sshd's ports are read" "[ \"\$(PATH=/tmp/fakebin:\$PATH firewall_ssh_ports | paste -sd,)\" = 2222,22022 ]"
  check "22 is the fallback when sshd is absent" "[ \"\$(PATH=/usr/bin:/bin firewall_ssh_ports)\" = 22 ]"
  PATH=/tmp/fakebin:$PATH firewall_write_host_table audio "" /tmp/host.nft
  check "the table accepts every sshd port" "grep -q 'tcp dport { 2222,22022 } accept' /tmp/host.nft"
  check "the table passes nft -c" "nft -c -f /tmp/host.nft"
  ;;

none)
  export OPT_FIREWALL=""
  firewall_setup render "" > /tmp/none.log 2>&1
  cat /tmp/none.log
  check "one line says so" "[ \"\$(grep -c . /tmp/none.log)\" = 1 ] && grep -q 'No firewall is active' /tmp/none.log"
  check "nothing was installed" "! command -v ufw >/dev/null 2>&1 && ! command -v firewall-cmd >/dev/null 2>&1"
  ;;

firewall-flag)
  apt-get install -y -qq --no-install-recommends docker.io docker-cli >/dev/null 2>&1
  (dockerd >/var/log/dockerd.log 2>&1 &)
  for _ in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
  if ! docker info >/dev/null 2>&1; then
    echo "dockerd did not start in this container; cannot prove Docker's tables survive"
    exit 1
  fi
  export OPT_FIREWALL=yes
  firewall_setup audio "" > /tmp/first.log 2>&1
  cat /tmp/first.log
  nft list tables > /tmp/tables.txt
  nft list table inet showmesh_host > /tmp/showmesh-table.txt
  check "a ShowMesh table is loaded" "grep -q 'table inet showmesh_host' /tmp/tables.txt"
  check "Docker's own tables survive" "grep -q 'table ip nat' /tmp/tables.txt && grep -q 'table ip filter' /tmp/tables.txt"
  check "the table's input chain policy is drop" "grep -q 'policy drop' /tmp/showmesh-table.txt"
  check "SSH and the role's ports are accepted" \
    "grep -q 'tcp dport 22 accept' /tmp/showmesh-table.txt && grep -q 'udp dport 32320 accept' /tmp/showmesh-table.txt"
  check "nftables.conf includes the ShowMesh directory" "grep -qF 'include \"/etc/nftables.d/*.nft\"' /etc/nftables.conf"

  firewall_setup audio "" > /tmp/second.log 2>&1
  cat /tmp/second.log
  nft list tables > /tmp/tables-2.txt
  check "a second --firewall run refreshes the table instead of warning" "grep -q 'a ShowMesh firewall is active' /tmp/second.log"
  check "Docker's tables still survive the second run" "grep -q 'table ip nat' /tmp/tables-2.txt && grep -q 'table ip filter' /tmp/tables-2.txt"
  ;;

no-firewall-flag)
  apt-get install -y -qq --no-install-recommends ufw >/dev/null 2>&1
  ufw --force enable >/dev/null 2>&1
  export OPT_FIREWALL=no
  firewall_setup render "" > /tmp/skip.log 2>&1
  cat /tmp/skip.log
  ufw status > /tmp/ufw-status.txt
  check "nothing was opened" "! grep -q 80/tcp /tmp/ufw-status.txt"
  check "the skip is reported" "grep -q -- '--no-firewall' /tmp/skip.log"
  ;;

*)
  echo "unknown case $CASE" >&2
  exit 2
  ;;
esac

echo
if [ "$FAILED" -gt 0 ]; then
  echo "run_firewall_lib_proof $CASE: $FAILED check(s) failed"
  exit 1
fi
echo "run_firewall_lib_proof $CASE: all checks passed"
