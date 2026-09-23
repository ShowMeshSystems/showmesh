# shellcheck shell=bash
# Audio node: the PTP-disciplined audio clock, via install-ptp-audio.sh.

ptp_script() {
  printf '%s/node/install-ptp-audio.sh' "$BUNDLE_DIR"
}

list_interfaces() {
  local n
  for n in /sys/class/net/*; do
    n="$(basename "$n")"
    [ "$n" = "lo" ] && continue
    printf '%s\n' "$n"
  done
}

ptp_later_hint() {
  info "To set it up later, run: sudo $(ptp_script) <interface> <domain> follower <sound-card-name>"
}

audio_setup_ptp() {
  local iface="$OPT_PTP_INTERFACE" domain="$OPT_PTP_DOMAIN" role="$OPT_PTP_ROLE" card="$OPT_AUDIO_CARD"
  step "Audio clock from PTP"
  if [ -z "$iface" ]; then
    if ! can_prompt; then
      info "Skipped the PTP audio clock because no --ptp-interface was given."
      ptp_later_hint
      return
    fi
    if ! confirm "Lock this node's sound card to the network's PTP clock now?" y; then
      info "Skipped the PTP audio clock."
      ptp_later_hint
      return
    fi
    info "Network interfaces on this machine:"
    list_interfaces | sed 's/^/      /'
    ask iface "Interface PTP runs on" "$(list_interfaces | head -n 1)"
    ask domain "PTP domain number" "${domain:-0}"
    ask role "PTP role: follower, grandmaster or auto" "${role:-follower}"
    info "Sound cards on this machine:"
    if [ -r /proc/asound/cards ]; then sed 's/^/      /' /proc/asound/cards; else info "  (none found)"; fi
    ask card "Part of the sound card's name to use" "${card:-M4}"
  fi
  domain="${domain:-0}"
  role="${role:-follower}"
  case "$role" in follower|grandmaster|auto) ;; *) fail "The PTP role $role is not follower, grandmaster or auto." "showmesh-install --ptp-role follower" ;; esac
  local args=("$iface" "$domain" "$role")
  [ -n "$card" ] && args+=("$card")
  if ! "$(ptp_script)" "${args[@]}"; then
    fail "The PTP audio clock setup stopped; the reason is printed above." "sudo $(ptp_script) ${args[*]}"
  fi
  ok "PTP audio clock set up on $iface, domain $domain, role $role"
}
