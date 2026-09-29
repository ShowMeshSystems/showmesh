# shellcheck shell=bash
# Firewall setup: opens the ports the installed role needs in an active ufw or
# firewalld, warns instead of touching a raw nftables ruleset with a drop
# policy, and with --firewall installs a ShowMesh host table when no firewall
# is active. See deploy/install/README.md for the port list and the reference
# nftables table.

FIREWALL_KIND=""
FIREWALL_NFT_TABLE=/etc/nftables.d/showmesh-host.nft
FIREWALL_NFT_CONF=/etc/nftables.conf

# firewall_configured_port FILE VAR DEFAULT prints the port from a saved
# "host:port" setting, or DEFAULT when the setting is absent.
firewall_configured_port() {
  local val
  val="$(env_get "$1" "$2")"
  [ -n "$val" ] && printf '%s' "${val##*:}" || printf '%s' "$3"
}

# firewall_node_ports KIND prints "proto|port|label" lines, one per port
# group, for a render or audio node.
firewall_node_ports() {
  local kind="$1" http_port sync_port
  http_port="$(firewall_configured_port "$AGENT_ENV" SHOWMESH_FPPCONNECT_LISTEN_ADDR 80)"
  sync_port="$(firewall_configured_port "$AGENT_ENV" SHOWMESH_MULTISYNC_LISTEN_ADDR 32320)"
  printf 'tcp|%s|node HTTP, FPP Connect uploads\n' "$http_port"
  printf 'udp|%s|FPP MultiSync\n' "$sync_port"
  printf 'udp|319|PTP\n'
  printf 'udp|320|PTP\n'
  printf 'udp|5004|AES67 RTP\n'
  printf 'udp|9875|AES67 SAP\n'
  printf 'udp|5353|mDNS\n'
  if [ "$kind" = render ]; then
    printf 'tcp|5959-5999|NDI\n'
    printf 'udp|5959-5999|NDI\n'
  fi
}

# firewall_coordinator_ports prints the coordinator's own ports. Docker
# publishes these itself, which bypasses this host's INPUT chain on ufw and
# nftables, but a firewalld zone can still block a published port, so they
# are opened on every backend rather than only where they are load-bearing.
firewall_coordinator_ports() {
  local http_port ui_port mqtt_port
  http_port="$(env_get "$COORD_ENV" SHOWMESH_HTTP_PORT)"
  ui_port="$(env_get "$COORD_ENV" SHOWMESH_UI_PORT)"
  printf 'tcp|%s|coordinator API\n' "${http_port:-8080}"
  printf 'tcp|%s|operator UI\n' "${ui_port:-8081}"
  if [ "$(env_get "$COORD_ENV" SHOWMESH_BROKER_MODE)" = builtin ]; then
    mqtt_port="$(env_get "$COORD_ENV" MOSQUITTO_PORT)"
    printf 'tcp|%s|built-in broker\n' "${mqtt_port:-1883}"
  fi
}

# firewall_ports_for_role ROLE KIND prints the port groups the installed role
# needs. KIND is the node half (render or audio); unused for other roles.
firewall_ports_for_role() {
  local role="$1" kind="$2"
  case "$role" in
    coordinator) firewall_coordinator_ports ;;
    render|audio) firewall_node_ports "$role" ;;
    coordinator-node)
      firewall_coordinator_ports
      firewall_node_ports "$kind"
      ;;
  esac
}

firewall_role_words() {
  case "$1" in
    coordinator) printf 'coordinator' ;;
    render) printf 'render node' ;;
    audio) printf 'audio node' ;;
    coordinator-node) printf 'coordinator and node' ;;
  esac
}

# firewall_detect sets FIREWALL_KIND to showmesh-host, ufw, firewalld,
# nftables-drop or none. showmesh-host (a previous --firewall run's own
# table) is checked first so a re-run refreshes it instead of mistaking it
# for a foreign ruleset this installer must not touch.
#
# Every check below captures a command's output into a variable before
# testing it, rather than piping straight into grep -q: showmesh-install
# runs under pipefail, and grep -q closes its input as soon as it has a
# match, which can SIGPIPE a still-writing nft or ufw and fail the pipeline
# even though the pattern was found.
firewall_detect() {
  local ufw_out="" nft_out=""
  if command -v ufw >/dev/null 2>&1; then
    ufw_out="$(ufw status 2>/dev/null || true)"
    case "$ufw_out" in
      "Status: active"*) FIREWALL_KIND=ufw; return ;;
    esac
  fi
  if command -v firewall-cmd >/dev/null 2>&1 && [ "$(firewall-cmd --state 2>/dev/null || true)" = running ]; then
    FIREWALL_KIND=firewalld
    return
  fi
  if command -v nft >/dev/null 2>&1; then
    nft_out="$(nft list ruleset 2>/dev/null || true)"
    case "$nft_out" in
      *'table inet showmesh_host'*) FIREWALL_KIND=showmesh-host; return ;;
    esac
    # grep matches per line; nft prints a chain's hook and policy on one line,
    # so another chain's "policy drop" (Docker's FORWARD) cannot match.
    if grep -qE 'hook input.*policy drop' <<<"$nft_out"; then
      FIREWALL_KIND=nftables-drop
      return
    fi
  fi
  FIREWALL_KIND=none
}

# firewall_ssh_ports prints every tcp port sshd listens on, or 22 when sshd
# does not say.
firewall_ssh_ports() {
  local ports
  ports="$({ sshd -T 2>/dev/null || true; } | awk '/^port /{print $2}')"
  printf '%s\n' "${ports:-22}"
}

# firewall_last_line FILE prints the last line a failed command printed.
firewall_last_line() {
  tail -n 1 "$1" | tr -d '\r'
}

firewall_apply_ufw() {
  local role="$1" kind="$2" proto port label spec out
  out="$(run_tmp)"
  step "Opening ShowMesh's ports in ufw"
  while IFS='|' read -r proto port label; do
    case "$port" in *-*) spec="${port%-*}:${port#*-}" ;; *) spec="$port" ;; esac
    if run_step "opening $proto $port in ufw" "$out" ufw allow "$spec/$proto" comment "ShowMesh: $label"; then
      ok "$proto $port ($label) is open in ufw"
    else
      warn "ufw did not open $proto $port ($label): $(firewall_last_line "$out") Add the rule with ufw allow $spec/$proto."
    fi
  done < <(firewall_ports_for_role "$role" "$kind")
}

firewall_write_firewalld_service() {
  local role="$1" kind="$2" proto port label
  printf '<?xml version="1.0" encoding="utf-8"?>\n<service>\n'
  printf '  <short>ShowMesh</short>\n'
  printf '  <description>Ports the %s role needs.</description>\n' "$(firewall_role_words "$role")"
  while IFS='|' read -r proto port label; do
    printf '  <port protocol="%s" port="%s"/>\n' "$proto" "$port"
  done < <(firewall_ports_for_role "$role" "$kind")
  printf '</service>\n'
}

# firewall_firewalld_zones prints every zone that has an interface, or the
# default zone when none does.
firewall_firewalld_zones() {
  local zones
  zones="$(firewall-cmd --get-active-zones 2>/dev/null | awk '/^[^ ]/{z=$1} /^ +interfaces:/{print z}' || true)"
  printf '%s\n' "${zones:-$(firewall-cmd --get-default-zone 2>/dev/null || true)}"
}

# firewall_firewalld_define_service makes the permanent showmesh service list
# this role's ports without a reload, which would discard runtime-only rules.
firewall_firewalld_define_service() {
  local role="$1" kind="$2" tmp proto port label rc=0
  if firewall-cmd --permanent --get-services 2>/dev/null | tr ' ' '\n' | grep -qx showmesh; then
    while IFS='|' read -r proto port label; do
      firewall-cmd --permanent --service=showmesh --add-port="$port/$proto" || rc=1
    done < <(firewall_ports_for_role "$role" "$kind")
    return "$rc"
  fi
  tmp="$RUN_TMP/showmesh-firewalld.xml"
  firewall_write_firewalld_service "$role" "$kind" > "$tmp"
  firewall-cmd --permanent --new-service-from-file="$tmp" --name=showmesh
}

firewall_apply_firewalld() {
  local role="$1" kind="$2" zone proto port label out failed=0
  out="$(run_tmp)"
  step "Opening ShowMesh's ports in firewalld"
  if ! run_step "defining the ShowMesh service in firewalld" "$out" firewall_firewalld_define_service "$role" "$kind"; then
    warn "firewalld did not accept the ShowMesh service definition: $(firewall_last_line "$out") Open the ports in your zones with firewall-cmd --add-port."
    return
  fi
  while IFS= read -r zone; do
    [ -n "$zone" ] || continue
    if ! run_step "adding ShowMesh to firewalld zone $zone" "$out" firewall-cmd --permanent --zone="$zone" --add-service=showmesh; then
      warn "firewalld did not add ShowMesh to zone $zone: $(firewall_last_line "$out") Run firewall-cmd --permanent --zone=$zone --add-service=showmesh."
      failed=1
      continue
    fi
    while IFS='|' read -r proto port label; do
      if ! run_step "opening $proto $port in firewalld zone $zone" "$out" firewall-cmd --zone="$zone" --add-port="$port/$proto"; then
        warn "firewalld did not open $proto $port in zone $zone: $(firewall_last_line "$out") Run firewall-cmd --zone=$zone --add-port=$port/$proto."
        failed=1
      fi
    done < <(firewall_ports_for_role "$role" "$kind")
  done < <(firewall_firewalld_zones)
  [ "$failed" = 0 ] || return 0
  while IFS='|' read -r proto port label; do
    ok "$proto $port ($label) is open in firewalld"
  done < <(firewall_ports_for_role "$role" "$kind")
}

firewall_warn_nftables() {
  local role="$1" kind="$2" proto port label
  warn "This host's nftables input chain drops traffic ShowMesh needs, so ShowMesh left your rules alone. Add these rules to your input chain, normally loaded from $FIREWALL_NFT_CONF:"
  while IFS='|' read -r proto port label; do
    info "  $proto dport $port accept   # ShowMesh: $label" >&2
  done < <(firewall_ports_for_role "$role" "$kind")
}

# firewall_role_nft_rules ROLE KIND prints the role's ports as nft rule lines,
# skipping udp/5353 and tcp/22 because the host table's fixed prefix already
# accepts both.
firewall_role_nft_rules() {
  local role="$1" kind="$2" proto port label
  while IFS='|' read -r proto port label; do
    case "$proto/$port" in udp/5353|tcp/22) continue ;; esac
    printf '        %s dport %s accept\n' "$proto" "$port"
  done < <(firewall_ports_for_role "$role" "$kind")
}

firewall_write_host_table() {
  local role="$1" kind="$2" dest="$3" rules ssh_ports
  rules="$(firewall_role_nft_rules "$role" "$kind")"
  ssh_ports="$(firewall_ssh_ports | paste -sd, -)"
  cat > "$dest" <<EOF
table inet showmesh_host
delete table inet showmesh_host
table inet showmesh_host {
    chain input {
        type filter hook input priority filter; policy drop;
        ct state established,related accept
        ct state invalid drop
        iif lo accept
        iifname { "docker0", "br-*" } accept
        meta l4proto { icmp, ipv6-icmp } accept
        ip protocol igmp accept
        udp dport 5353 accept
        tcp dport { $ssh_ports } accept
$rules
    }
}
EOF
}

# firewall_ensure_nftables_conf_includes makes nftables.conf load
# /etc/nftables.d/*.nft, backing up an existing file the first time only.
firewall_ensure_nftables_conf_includes() {
  local include_line='include "/etc/nftables.d/*.nft"'
  if [ -f "$FIREWALL_NFT_CONF" ]; then
    grep -qxF "$include_line" "$FIREWALL_NFT_CONF" && return 0
    [ -f "$FIREWALL_NFT_CONF.showmesh-orig" ] || cp -p "$FIREWALL_NFT_CONF" "$FIREWALL_NFT_CONF.showmesh-orig"
    printf '%s\n' "$include_line" >> "$FIREWALL_NFT_CONF"
  else
    printf '#!/usr/sbin/nft -f\n\n%s\n' "$include_line" > "$FIREWALL_NFT_CONF"
    chmod 0755 "$FIREWALL_NFT_CONF"
  fi
}

# firewall_conf_has_unloaded_rules succeeds when nftables.conf declares a table
# other than ShowMesh's that is not loaded now, so enabling the nftables
# service would load rules the operator is not running today.
firewall_conf_has_unloaded_rules() {
  local loaded fam name
  [ -f "$FIREWALL_NFT_CONF" ] || return 1
  loaded="$(nft list tables 2>/dev/null || true)"
  while read -r fam name; do
    [ "$name" = showmesh_host ] && continue
    grep -qx "table $fam $name" <<<"$loaded" || return 0
  done < <(awk '$1=="table" && NF>=3 {print $2, $3}' "$FIREWALL_NFT_CONF")
  return 1
}

firewall_install_host_table() {
  local role="$1" kind="$2" candidate="$FIREWALL_NFT_TABLE.new" enable=yes out
  out="$(run_tmp)"
  step "Installing a ShowMesh firewall"
  apt_install nftables
  mkdir -p "$(dirname "$FIREWALL_NFT_TABLE")"
  firewall_write_host_table "$role" "$kind" "$candidate"
  # Loaded as just this table: nft -f on the full nftables.conf would run that
  # file's own "flush ruleset" and take Docker's tables down with it.
  run_step "checking the ShowMesh firewall table" "$out" with_default_umask nft -c -f "$candidate" || {
    rm -f "$candidate"
    fail_with "$out" "The ShowMesh firewall table has a syntax error." "nft -c -f $FIREWALL_NFT_TABLE"
  }
  mv -f "$candidate" "$FIREWALL_NFT_TABLE"
  if firewall_conf_has_unloaded_rules; then
    enable=no
  fi
  firewall_ensure_nftables_conf_includes
  run_step "loading the ShowMesh firewall table" "$out" with_default_umask nft -f "$FIREWALL_NFT_TABLE" ||
    fail_with "$out" "The ShowMesh firewall table could not be loaded." "nft -f $FIREWALL_NFT_TABLE"
  if [ "$enable" = yes ]; then
    have_systemd && { systemctl enable nftables >/dev/null 2>&1 || true; }
  else
    warn "$FIREWALL_NFT_CONF holds rules that are not loaded, so the ShowMesh firewall will not start at boot. Load or remove those rules, then run systemctl enable nftables."
  fi
  ok "a ShowMesh firewall is active; SSH, loopback and Docker stay open, and everything else the $(firewall_role_words "$role") needs is allowed"
}

# firewall_setup ROLE KIND is showmesh-install's one hook into this file.
# KIND is the node half of a coordinator-node install (render or audio);
# pass "" for every other role.
firewall_setup() {
  local role="$1" kind="${2:-}"
  if [ "$OPT_FIREWALL" = no ]; then
    info "Skipped firewall setup because --no-firewall was given."
    return
  fi
  firewall_detect
  case "$FIREWALL_KIND" in
    showmesh-host) firewall_install_host_table "$role" "$kind" ;;
    ufw) firewall_apply_ufw "$role" "$kind" ;;
    firewalld) firewall_apply_firewalld "$role" "$kind" ;;
    nftables-drop) firewall_warn_nftables "$role" "$kind" ;;
    none)
      if [ "$OPT_FIREWALL" = yes ]; then
        firewall_install_host_table "$role" "$kind"
      else
        info "No firewall is active on this host. Nothing was changed; run showmesh-install --firewall to add one."
      fi
      ;;
  esac
}
