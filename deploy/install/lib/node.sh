# shellcheck shell=bash
# Render node and audio node roles: packages, agent, enrollment, first report.

NODE_RUNTIME_PACKAGES=(
  alsa-utils gstreamer1.0-tools
  gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad
  gstreamer1.0-plugins-base-apps gstreamer1.0-alsa libltc11
)

node_install_packages() {
  step "Installing the node's runtime packages"
  apt_install ca-certificates curl jq iproute2 "${NODE_RUNTIME_PACKAGES[@]}"
  ok "runtime packages installed"
}

# node_fetch_agent sets AGENT_PKG_DIR to the unpacked node agent package.
node_fetch_agent() {
  local name tmp dest
  name="showmesh-node-agent_${SHOWMESH_VERSION}_linux_${ARCH}.tar.gz"
  tmp="$(mktemp -d)"
  if [ -n "$OPT_AGENT_PACKAGE" ]; then
    step "Using the node agent package $OPT_AGENT_PACKAGE"
    [ -f "$OPT_AGENT_PACKAGE" ] || fail "There is no file at $OPT_AGENT_PACKAGE." "showmesh-install --agent-package <file>"
    info "This package is not checked against a release, because it came from this machine."
    cp "$OPT_AGENT_PACKAGE" "$tmp/$name"
  else
    step "Downloading the node agent $SHOWMESH_VERSION for $ARCH"
    release_fetch "$name" "$tmp/$name"
    release_verify "$name" "$tmp/$name"
  fi
  dest="$NODE_DIR/$SHOWMESH_VERSION"
  rm -rf "$dest.new"
  mkdir -p "$dest.new"
  tar -xzf "$tmp/$name" -C "$dest.new"
  rm -rf "$dest" "$tmp"
  mv "$dest.new" "$dest"
  AGENT_PKG_DIR="$dest/showmesh-node-agent"
  [ -x "$AGENT_PKG_DIR/showmesh-agent-native" ] ||
    fail "The node agent package has no showmesh-agent-native program." "check that version $SHOWMESH_VERSION is a complete release"
  ok "unpacked to $AGENT_PKG_DIR"
}

node_preflight() {
  step "Checking this machine can run the agent"
  if ! "$AGENT_PKG_DIR/preflight.sh" --runtime-only; then
    fail "This machine is missing something the agent needs, listed above." "apt-get install -y ${NODE_RUNTIME_PACKAGES[*]}"
  fi
}

node_is_enrolled() {
  [ -n "$(env_get "$AGENT_ENV" SHOWMESH_NODE_ID)" ] && [ -n "$(env_get "$AGENT_ENV" SHOWMESH_MQTT_BROKER)" ]
}

normalize_url() {
  local u="$1"
  case "$u" in http://*|https://*) ;; *) u="http://$u" ;; esac
  printf '%s' "${u%/}"
}

# discover_coordinators prints one http://address:port per coordinator avahi finds.
discover_coordinators() {
  command -v avahi-browse >/dev/null 2>&1 || return 0
  timeout 8 avahi-browse -rtp _showmesh._tcp 2>/dev/null |
    awk -F';' '$1 == "=" && $3 == "IPv4" {print "http://" $8 ":" $9}' | sort -u
}

choose_coordinator() {
  if [ -n "$OPT_COORDINATOR" ]; then
    COORDINATOR_URL="$(normalize_url "$OPT_COORDINATOR")"
    return
  fi
  local saved
  saved="$(env_get "$INSTALLER_STATE" COORDINATOR_URL)"
  if ! can_prompt; then
    [ -n "$saved" ] && { COORDINATOR_URL="$saved"; return; }
    need_answer "the coordinator's address" "--coordinator http://<address>:8080"
  fi
  step "Finding the coordinator"
  if ! command -v avahi-browse >/dev/null 2>&1; then
    apt_install avahi-daemon avahi-utils
  fi
  local found=() line n pick
  while IFS= read -r line; do [ -n "$line" ] && found+=("$line"); done < <(discover_coordinators)
  if [ "${#found[@]}" -gt 0 ]; then
    info "Coordinators found on this network:"
    n=1
    for line in "${found[@]}"; do info "  $n) $line"; n=$((n + 1)); done
    ask pick "Pick a number, or type an address" "1"
    if [[ "$pick" =~ ^[0-9]+$ ]] && [ "$pick" -ge 1 ] && [ "$pick" -le "${#found[@]}" ]; then
      COORDINATOR_URL="${found[$((pick - 1))]}"
      return
    fi
    COORDINATOR_URL="$(normalize_url "$pick")"
    return
  fi
  info "No coordinator announced itself on this network."
  ask pick "Coordinator address, for example 192.168.1.10:8080" "$saved"
  [ -z "$pick" ] && fail "No coordinator address was given." "showmesh-install --coordinator http://<address>:8080"
  COORDINATOR_URL="$(normalize_url "$pick")"
}

valid_code() {
  local c="${1//-/}"
  [[ "$c" =~ ^[A-Za-z0-9]{8}$ ]]
}

# redeem_code CODE redeems once. Returns 0 on success, 1 for a code problem, 2 when unreachable.
redeem_code() {
  local body
  body="$(jq -nc --arg code "$1" --arg hostname "$(hostname)" --arg arch "$ARCH" \
    '{code: $code, hostname: $hostname, arch: $arch}')"
  http_request POST "$COORDINATOR_URL/api/v1/node-enrollments/redeem" "$body"
  case "$HTTP_STATUS" in
    200) return 0 ;;
    000) return 2 ;;
    429)
      info "$(problem_detail)"
      [ -n "$HTTP_RETRY_AFTER" ] && info "The coordinator accepts another try in $HTTP_RETRY_AFTER seconds."
      return 1 ;;
    *) info "$(problem_detail)"; return 1 ;;
  esac
}

enroll_values_from_redeem() {
  NODE_ID="$(printf '%s' "$HTTP_BODY" | jq -r '.nodeId // empty')"
  BROKER_URL="$(printf '%s' "$HTTP_BODY" | jq -r '.brokerUrl // empty')"
  MQTT_USERNAME="$(printf '%s' "$HTTP_BODY" | jq -r '.mqttUsername // empty')"
  MQTT_PASSWORD="$(printf '%s' "$HTTP_BODY" | jq -r '.mqttPassword // empty')"
  API_TOKEN="$(printf '%s' "$HTTP_BODY" | jq -r '.apiToken // empty')"
  COORDINATOR_PUBLIC_KEY="$(printf '%s' "$HTTP_BODY" | jq -r '.coordinatorPublicKey // empty')"
  local url
  url="$(printf '%s' "$HTTP_BODY" | jq -r '.coordinatorUrl // empty')"
  [ -n "$url" ] && COORDINATOR_URL="$(normalize_url "$url")"
  if [ -z "$NODE_ID" ] || [ -z "$BROKER_URL" ] || [ -z "$API_TOKEN" ]; then
    fail "The coordinator accepted the code but its answer is missing the node's settings." "showmeshctl node enroll <node-id> on the coordinator, then run this installer again with the new code"
  fi
}

enroll_values_by_hand() {
  info "Type the node's settings as the coordinator's operator gave them to you."
  ask NODE_ID "Node ID" "$(hostname | tr 'A-Z_' 'a-z-')"
  ask BROKER_URL "Broker address, for example tcp://192.168.1.10:1883"
  ask MQTT_USERNAME "Broker username (leave empty for none)" "$NODE_ID"
  ask_secret MQTT_PASSWORD "Broker password (leave empty for none)"
  ask_secret API_TOKEN "API token for this node"
  COORDINATOR_PUBLIC_KEY=""
  [ -n "$NODE_ID" ] && [ -n "$BROKER_URL" ] || fail "The node ID and broker address are both required." "showmesh-install --reenroll"
}

node_enroll() {
  step "Enrolling this node with the coordinator"
  local code="$OPT_CODE" tries=0 rc
  while :; do
    if [ -z "$code" ]; then
      can_prompt || need_answer "an enrollment code" "--code XXXX-XXXX"
      info "On the coordinator, run: showmeshctl node enroll <node-id>"
      ask code "Enrollment code"
    fi
    if ! valid_code "$code"; then
      info "The code $code is not in the form XXXX-XXXX."
      can_prompt || fail "The enrollment code $code is not in the form XXXX-XXXX." "showmesh-install --code XXXX-XXXX"
      code=""
      continue
    fi
    rc=0
    redeem_code "$code" || rc=$?
    if [ "$rc" -eq 0 ]; then
      enroll_values_from_redeem
      ok "enrolled as node $NODE_ID"
      return
    fi
    if [ "$rc" -eq 2 ]; then
      if can_prompt && confirm "The coordinator at $COORDINATOR_URL did not answer. Type the node's settings by hand instead?" n; then
        enroll_values_by_hand
        return
      fi
      fail "The coordinator at $COORDINATOR_URL did not answer." "curl -sS $COORDINATOR_URL/healthz"
    fi
    tries=$((tries + 1))
    if ! can_prompt || [ "$tries" -ge 3 ]; then
      fail "The enrollment code was not accepted." "showmeshctl node enroll <node-id> on the coordinator, then run showmesh-install --code <new code>"
    fi
    code=""
  done
}

node_write_env() {
  step "Writing the node's settings to $AGENT_ENV"
  mkdir -p "$ETC_DIR"
  chmod 0755 "$ETC_DIR"
  if [ ! -f "$AGENT_ENV" ]; then
    install -m 0600 -o root -g root "$AGENT_PKG_DIR/agent.env.example" "$AGENT_ENV"
  fi
  local pairs=(
    "SHOWMESH_NODE_ID=$NODE_ID"
    "SHOWMESH_MQTT_BROKER=$BROKER_URL"
    "SHOWMESH_MQTT_USERNAME=$MQTT_USERNAME"
    "SHOWMESH_MQTT_PASSWORD=$MQTT_PASSWORD"
    "SHOWMESH_AGENT_API_TOKEN=$API_TOKEN"
  )
  if [ -n "$COORDINATOR_PUBLIC_KEY" ]; then
    printf '%s\n' "$COORDINATOR_PUBLIC_KEY" > "$ETC_DIR/coordinator-public.key"
    chmod 0644 "$ETC_DIR/coordinator-public.key"
    pairs+=("SHOWMESH_WEATHERDELAY_COORDINATOR_PUBLIC_KEY_PATH=$ETC_DIR/coordinator-public.key")
  fi
  env_set "$AGENT_ENV" 0600 "${pairs[@]}"
  ok "settings written; every other line in $AGENT_ENV is unchanged"
}

node_state_save() {
  env_set "$INSTALLER_STATE" 0600 "ROLE=$ROLE" "VERSION=$SHOWMESH_VERSION" "COORDINATOR_URL=$COORDINATOR_URL"
}

# node_started_at prints the node's startedAt as the coordinator reports it, or nothing.
node_started_at() {
  http_request GET "$COORDINATOR_URL/api/v1/nodes/$1" "" "$2"
  [ "$HTTP_STATUS" = "200" ] || return 0
  printf '%s' "$HTTP_BODY" | jq -r '.node.startedAt // empty'
}

node_install_agent() {
  step "Installing the agent service"
  if ! "$AGENT_PKG_DIR/install.sh" "$AGENT_PKG_DIR/showmesh-agent-native"; then
    fail "The agent service did not install; the reason is printed above." "sudo $AGENT_PKG_DIR/install.sh $AGENT_PKG_DIR/showmesh-agent-native"
  fi
  if ! have_systemd; then
    warn "This machine is not running systemd, so the agent was not started. Start it with: systemctl enable --now showmesh-agent"
  fi
}

# node_wait_for_report waits for the node to report in after BEFORE (its previous startedAt).
node_wait_for_report() {
  local node_id="$1" token="$2" before="$3" limit="${SHOWMESH_REPORT_TIMEOUT:-120}" waited=0 state started
  step "Waiting for node $node_id to report to the coordinator"
  while [ "$waited" -lt "$limit" ]; do
    http_request GET "$COORDINATOR_URL/api/v1/nodes/$node_id" "" "$token"
    if [ "$HTTP_STATUS" = "200" ]; then
      state="$(printf '%s' "$HTTP_BODY" | jq -r '.node.controlPlane.state // empty')"
      started="$(printf '%s' "$HTTP_BODY" | jq -r '.node.startedAt // empty')"
      if [ "$state" = "online" ] && [ -n "$started" ] && [ "$started" != "$before" ]; then
        ok "node $node_id is online, agent started at $started"
        return 0
      fi
    fi
    sleep 3
    waited=$((waited + 3))
  done
  fail "Node $node_id has not reported to the coordinator at $COORDINATOR_URL after $limit seconds (last answer: HTTP $HTTP_STATUS)." "journalctl -u showmesh-agent -n 50"
}

run_node_role() {
  local kind="$1" before=""
  node_install_packages
  node_fetch_agent
  node_preflight
  [ "$kind" = "render" ] && render_install_ndi_plugin
  if node_is_enrolled && [ "$OPT_REENROLL" -eq 0 ]; then
    NODE_ID="$(env_get "$AGENT_ENV" SHOWMESH_NODE_ID)"
    API_TOKEN="$(env_get "$AGENT_ENV" SHOWMESH_AGENT_API_TOKEN)"
    COORDINATOR_URL="$(env_get "$INSTALLER_STATE" COORDINATOR_URL)"
    [ -n "$OPT_COORDINATOR" ] && COORDINATOR_URL="$(normalize_url "$OPT_COORDINATOR")"
    step "This node is already enrolled as $NODE_ID"
    info "Its settings are kept. To enroll it again, run: showmesh-install --reenroll"
  else
    choose_coordinator
    node_enroll
    node_write_env
  fi
  node_state_save
  [ -n "$COORDINATOR_URL" ] && before="$(node_started_at "$NODE_ID" "$API_TOKEN")"
  node_install_agent
  [ "$kind" = "render" ] && render_ndi_runtime
  [ "$kind" = "audio" ] && audio_setup_ptp
  if [ -z "$COORDINATOR_URL" ]; then
    fail "This node has no coordinator address saved, so its first report cannot be checked." "showmesh-install --coordinator http://<address>:8080"
  fi
  if [ -z "$API_TOKEN" ]; then
    warn "This node has no API token, so the installer cannot ask the coordinator whether it reported in. Check the node list in the coordinator's UI."
    return 0
  fi
  node_wait_for_report "$NODE_ID" "$API_TOKEN" "$before"
}
