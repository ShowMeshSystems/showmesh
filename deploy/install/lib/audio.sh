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

# audio_setup_ptp DEFAULT sets up the PTP audio clock. DEFAULT is the answer to "now?":
# y on an audio node, n on a render node, which needs it only when it also plays audio.
audio_setup_ptp() {
  local want="$1" iface="$OPT_PTP_INTERFACE" domain="$OPT_PTP_DOMAIN" role="$OPT_PTP_ROLE" card="$OPT_AUDIO_CARD"
  [ -z "$iface" ] && [ "$want" = n ] && ! can_prompt && return 0
  step "Audio clock from PTP"
  if [ -z "$iface" ]; then
    if ! can_prompt; then
      info "Skipped the PTP audio clock because no --ptp-interface was given."
      ptp_later_hint
      return
    fi
    [ "$want" = n ] && info "A render node needs this only when it also plays audio."
    if ! confirm "Lock this node's sound card to the network's PTP clock now?" "$want"; then
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
  local args=("$iface" "$domain" "$role") log
  [ -n "$card" ] && args+=("$card")
  log="$(run_tmp)"
  if ! run_step "setting up the PTP audio clock" "$log" with_default_umask "$(ptp_script)" "${args[@]}"; then
    fail_with "$log" "The PTP audio clock setup stopped." "sudo $(ptp_script) ${args[*]}"
  fi
  step_warnings "$log"
  ok "PTP audio clock set up on $iface, domain $domain, role $role"
}
