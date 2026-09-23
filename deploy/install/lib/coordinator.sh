# shellcheck shell=bash
# Coordinator role: Docker, the Compose bundle, the broker, the first administrator and showmeshctl.

COORD_ENV="$COORDINATOR_DIR/.env"
CTL_ENV="$ETC_DIR/showmeshctl.env"
CTL_BIN=/usr/local/lib/showmesh/showmeshctl
MIN_COMPOSE=2.24.0

compose() {
  local files=(-f docker-compose.yml -f docker-compose.published.yml)
  (cd "$COORDINATOR_DIR" && docker compose "${files[@]}" "$@")
}

version_at_least() {
  [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n 1)" = "$2" ]
}

coord_install_docker() {
  step "Installing Docker from Debian's own packages"
  apt_install ca-certificates curl jq iproute2 docker.io docker-cli docker-compose avahi-daemon
  if ! docker info >/dev/null 2>&1; then
    if have_systemd; then
      systemctl enable --now docker >/dev/null 2>&1 || true
    fi
  fi
  docker info >/dev/null 2>&1 || fail "Docker is installed but not running." "systemctl enable --now docker"
  local v
  v="$(docker compose version --short 2>/dev/null | sed 's/^v//')"
  [ -n "$v" ] || fail "The docker compose command is missing." "apt-get install -y docker-compose"
  version_at_least "$v" "$MIN_COMPOSE" ||
    fail "Docker Compose $v is older than $MIN_COMPOSE, which the ShowMesh bundle needs." "apt-get install -y -t trixie docker-compose"
  ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null) with Compose $v"
}

coord_install_bundle() {
  step "Placing the coordinator bundle in $COORDINATOR_DIR"
  local src="$BUNDLE_DIR/coordinator" f
  mkdir -p "$COORDINATOR_DIR/mosquitto"
  for f in docker-compose.yml docker-compose.published.yml; do
    install -m 0644 "$src/$f" "$COORDINATOR_DIR/$f"
  done
  for f in mosquitto.conf acl.conf; do
    install -m 0644 "$src/mosquitto/$f" "$COORDINATOR_DIR/mosquitto/$f"
  done
  for f in generate-credentials.sh add-agent-credential.sh; do
    install -m 0755 "$src/mosquitto/$f" "$COORDINATOR_DIR/mosquitto/$f"
  done
  ok "bundle files updated; .env and broker logins are kept"
}

coord_choose_broker() {
  BROKER_MODE="${OPT_BROKER:-$(env_get "$COORD_ENV" SHOWMESH_BROKER_MODE)}"
  if [ -z "$BROKER_MODE" ]; then
    if can_prompt; then
      step "Choosing the broker"
      info "1) Built-in broker (recommended). ShowMesh runs it and gives every node its own login."
      info "2) External broker you already run."
      local pick
      ask pick "Broker" "1"
      case "$pick" in 2|external) BROKER_MODE=external ;; *) BROKER_MODE=builtin ;; esac
    else
      BROKER_MODE=builtin
    fi
  fi
  case "$BROKER_MODE" in
    builtin) ;;
    external) coord_ask_external_broker ;;
    *) fail "The broker choice $BROKER_MODE is not builtin or external." "showmesh-install --broker builtin" ;;
  esac
}

coord_ask_external_broker() {
  EXT_BROKER_URL="${OPT_BROKER_URL:-$(env_get "$COORD_ENV" SHOWMESH_NODE_BROKER_URL)}"
  EXT_BROKER_USER="${OPT_BROKER_USERNAME-$(env_get "$COORD_ENV" SHOWMESH_NODE_MQTT_USERNAME)}"
  EXT_BROKER_PASS="${OPT_BROKER_PASSWORD-$(env_get "$COORD_ENV" SHOWMESH_NODE_MQTT_PASSWORD)}"
  if [ -z "$EXT_BROKER_URL" ]; then
    can_prompt || need_answer "the external broker's address" "--broker-url tcp://<address>:1883"
    ask EXT_BROKER_URL "External broker address, for example tcp://192.168.1.5:1883"
    ask EXT_BROKER_USER "Broker username (leave empty for none)"
    [ -n "$EXT_BROKER_USER" ] && ask_secret EXT_BROKER_PASS "Broker password"
  fi
  case "$EXT_BROKER_URL" in
    tcp://*|mqtt://*|ssl://*|tls://*|mqtts://*) ;;
    *) EXT_BROKER_URL="tcp://$EXT_BROKER_URL" ;;
  esac
  if [ "$(env_get "$COORD_ENV" SHOWMESH_BROKER_MODE)" = "external" ]; then
    return
  fi
  cat <<EOF

    Warning: on an external broker, any client that can reach it can command
    every ShowMesh node. ShowMesh does not create or manage that broker's users,
    and the built-in broker's per-node access rules do not apply there.

EOF
  if can_prompt; then
    local answer
    ask answer "Type yes to use the external broker anyway"
    [ "$answer" = "yes" ] || fail "The external broker was not confirmed." "showmesh-install --broker builtin"
  elif [ "$OPT_YES" -ne 1 ]; then
    fail "The external broker must be confirmed." "showmesh-install --broker external --yes"
  fi
}

# apply_broker_config_ownership lets the coordinator (uid 65532) rewrite the broker's logins while mosquitto (gid 1883) reads them.
# The setgid directory keeps group 1883 on files the coordinator replaces by rename.
apply_broker_config_ownership() {
  local dir="$COORDINATOR_DIR/mosquitto"
  chown 65532:1883 "$dir"
  chmod 2755 "$dir"
  if [ -f "$dir/passwd" ]; then chown 65532:1883 "$dir/passwd"; chmod 0640 "$dir/passwd"; fi
  if [ -f "$dir/acl.generated.conf" ]; then chown 65532:1883 "$dir/acl.generated.conf"; chmod 0644 "$dir/acl.generated.conf"; fi
  return 0
}

coord_write_env() {
  step "Writing the coordinator's settings to $COORD_ENV"
  local addr http_port mqtt_port
  addr="${OPT_ADDRESS:-$(lan_address)}"
  http_port="$(env_get "$COORD_ENV" SHOWMESH_HTTP_PORT)"
  mqtt_port="$(env_get "$COORD_ENV" MOSQUITTO_PORT)"
  HTTP_PORT="${http_port:-8080}"
  UI_PORT="$(env_get "$COORD_ENV" SHOWMESH_UI_PORT)"
  UI_PORT="${UI_PORT:-8081}"
  PUBLIC_URL="http://$addr:$HTTP_PORT"
  local pairs=(
    "COMPOSE_PROJECT_NAME=showmesh"
    "SHOWMESH_RELEASE_VERSION=$SHOWMESH_VERSION"
    "SHOWMESH_VERSION=$SHOWMESH_VERSION"
    "SHOWMESH_COMMIT=$SHOWMESH_COMMIT"
    "SHOWMESH_BUILD_DATE=$SHOWMESH_BUILD_DATE"
    "SHOWMESH_BROKER_MODE=$BROKER_MODE"
    "SHOWMESH_PUBLIC_URL=$PUBLIC_URL"
  )
  if [ "$BROKER_MODE" = "builtin" ]; then
    pairs+=(
      "SHOWMESH_NODE_BROKER_URL=tcp://$addr:${mqtt_port:-1883}"
      "SHOWMESH_MQTT_BROKER=tcp://mosquitto:1883"
      "SHOWMESH_NODE_MQTT_USERNAME="
      "SHOWMESH_NODE_MQTT_PASSWORD="
    )
  else
    pairs+=(
      "SHOWMESH_NODE_BROKER_URL=$EXT_BROKER_URL"
      "SHOWMESH_MQTT_BROKER=$EXT_BROKER_URL"
      "SHOWMESH_NODE_MQTT_USERNAME=$EXT_BROKER_USER"
      "SHOWMESH_NODE_MQTT_PASSWORD=$EXT_BROKER_PASS"
      "SHOWMESH_MQTT_USERNAME=$EXT_BROKER_USER"
      "SHOWMESH_MQTT_PASSWORD=$EXT_BROKER_PASS"
    )
  fi
  mkdir -p "$COORDINATOR_DIR"
  env_set "$COORD_ENV" 0600 "${pairs[@]}"
  ok "nodes will reach this coordinator at $PUBLIC_URL"
}

coord_builtin_credentials() {
  step "Creating the built-in broker's logins"
  if ! "$COORDINATOR_DIR/mosquitto/generate-credentials.sh" >/tmp/showmesh-broker.log 2>&1; then
    cat /tmp/showmesh-broker.log >&2
    fail "The broker logins could not be created; the reason is printed above." "sudo $COORDINATOR_DIR/mosquitto/generate-credentials.sh"
  fi
  if grep -q '^  password: ' /tmp/showmesh-broker.log; then
    info "FPP's broker login, shown once. Enter it in each FPP player under System Configuration, MQTT:"
    sed -n 's/^  \(username\|password\): /      \1: /p' /tmp/showmesh-broker.log
  fi
  rm -f /tmp/showmesh-broker.log
  apply_broker_config_ownership
  ok "broker logins are in $COORDINATOR_DIR/mosquitto/passwd"
}

coord_start() {
  step "Starting the coordinator $SHOWMESH_VERSION"
  local log=/tmp/showmesh-compose.log
  if [ "$BROKER_MODE" = "builtin" ]; then
    compose up -d --remove-orphans >"$log" 2>&1 || { tail -n 20 "$log" >&2; fail "The coordinator did not start." "cd $COORDINATOR_DIR && docker compose -f docker-compose.yml -f docker-compose.published.yml up -d"; }
  else
    compose rm -sf mosquitto >/dev/null 2>&1 || true
    compose up -d --no-deps coordinator ui >"$log" 2>&1 || { tail -n 20 "$log" >&2; fail "The coordinator did not start." "cd $COORDINATOR_DIR && docker compose -f docker-compose.yml -f docker-compose.published.yml up -d --no-deps coordinator ui"; }
  fi
  local waited=0
  while [ "$waited" -lt 120 ]; do
    http_request GET "http://127.0.0.1:$HTTP_PORT/healthz"
    [ "$HTTP_STATUS" = "200" ] && { ok "the coordinator answers on port $HTTP_PORT"; return; }
    sleep 2
    waited=$((waited + 2))
  done
  fail "The coordinator did not answer within 120 seconds." "cd $COORDINATOR_DIR && docker compose -f docker-compose.yml -f docker-compose.published.yml logs coordinator"
}

coord_ctl_token_works() {
  local token
  token="$(env_get "$CTL_ENV" SHOWMESH_CTL_TOKEN)"
  [ -n "$token" ] || return 1
  http_request GET "http://127.0.0.1:$HTTP_PORT/api/v1/nodes" "" "$token"
  [ "$HTTP_STATUS" = "200" ]
}

coord_exec() {
  compose exec -T coordinator /usr/local/bin/showmesh-coordinator "$@"
}

coord_install_ctl() {
  local src="$BUNDLE_DIR/bin/showmeshctl_linux_$ARCH"
  [ -f "$src" ] || fail "This installer has no showmeshctl for $ARCH." "download a complete installer for version $SHOWMESH_VERSION"
  install -D -m 0755 -o root -g root "$src" "$CTL_BIN"
  install -m 0755 -o root -g root "$BUNDLE_DIR/showmeshctl-wrapper.sh" /usr/local/bin/showmeshctl
}

coord_admin() {
  step "Setting up the first administrator and showmeshctl"
  coord_install_ctl
  if coord_ctl_token_works; then
    ok "showmeshctl already signs in as an administrator"
    ADMIN_READY=1
    return
  fi
  local name="$OPT_ADMIN_NAME" password=""
  if [ -n "$OPT_ADMIN_PASSWORD_FILE" ]; then
    [ -r "$OPT_ADMIN_PASSWORD_FILE" ] || fail "There is no readable file at $OPT_ADMIN_PASSWORD_FILE." "showmesh-install --admin-password-file <file>"
    password="$(head -n 1 "$OPT_ADMIN_PASSWORD_FILE")"
  fi
  if [ -z "$name" ] || [ -z "$password" ]; then
    if ! can_prompt; then
      ADMIN_READY=0
      info "No administrator was created because --admin-name and --admin-password-file were not given."
      info "Create one in the UI, or run: sudo showmesh-install --role coordinator from a terminal"
      return
    fi
    [ -z "$name" ] && ask name "Administrator name" "admin"
    while [ -z "$password" ]; do
      local again
      ask_secret password "Administrator password"
      ask_secret again "Type the password again"
      if [ "$password" != "$again" ] || [ -z "$password" ]; then
        info "The passwords were empty or did not match. Try again."
        password=""
      fi
    done
  fi
  local out
  if ! out="$(printf '%s\n' "$password" | coord_exec bootstrap -name "$name" -device-label installer 2>&1)"; then
    if ! out="$(printf '%s\n' "$password" | coord_exec create-admin -name "$name" 2>&1)"; then
      printf '%s\n' "$out" >&2
      fail "The administrator $name could not be created." "cd $COORDINATOR_DIR && docker compose -f docker-compose.yml -f docker-compose.published.yml exec coordinator /usr/local/bin/showmesh-coordinator create-admin -name $name"
    fi
  fi
  ok "created administrator $name"
  local token
  out="$(coord_exec issue-token -principal "$name" -label "showmeshctl on $(hostname)" 2>&1)" ||
    { printf '%s\n' "$out" >&2; fail "A token for showmeshctl could not be issued." "showmeshctl token issue <principal-id>"; }
  token="$(printf '%s\n' "$out" | awk 'NF {last = $0} END {print last}')"
  mkdir -p "$ETC_DIR"
  env_set "$CTL_ENV" 0600 "SHOWMESH_SERVER=http://127.0.0.1:$HTTP_PORT" "SHOWMESH_CTL_TOKEN=$token"
  coord_ctl_token_works || fail "The coordinator refused the token issued for showmeshctl." "showmeshctl token issue <principal-id>"
  ok "showmeshctl on this machine signs in as $name"
  ADMIN_READY=1
}

coord_check_broker() {
  step "Checking the broker accepts the coordinator's login"
  local user pass
  if [ "$BROKER_MODE" = "builtin" ]; then
    user="$(env_get "$COORD_ENV" SHOWMESH_MQTT_USERNAME)"
    pass="$(env_get "$COORD_ENV" SHOWMESH_MQTT_PASSWORD)"
    local waited=0
    # shellcheck disable=SC2016
    while ! MQ_USER="$user" MQ_PASS="$pass" compose exec -T -e MQ_USER -e MQ_PASS mosquitto \
      sh -c 'mosquitto_pub -h localhost -p 1883 -u "$MQ_USER" -P "$MQ_PASS" -i showmesh-installer-check -t showmesh/installer/check -n' >/dev/null 2>&1; do
      waited=$((waited + 2))
      [ "$waited" -ge 60 ] && fail "The built-in broker refused the coordinator's login." "cd $COORDINATOR_DIR && docker compose -f docker-compose.yml -f docker-compose.published.yml logs mosquitto"
      sleep 2
    done
  else
    local hostport="${EXT_BROKER_URL#*://}"
    local auth=()
    [ -n "$EXT_BROKER_USER" ] && auth=(-u "$EXT_BROKER_USER" -P "$EXT_BROKER_PASS")
    if ! docker run --rm --network host eclipse-mosquitto:2.0.22 mosquitto_pub -h "${hostport%:*}" -p "${hostport##*:}" \
      "${auth[@]}" -i showmesh-installer-check -t showmesh/installer/check -n >/dev/null 2>&1; then
      fail "The external broker at $EXT_BROKER_URL refused the login or did not answer." "showmesh-install --broker external --broker-url <address>"
    fi
  fi
  ok "the broker accepts the coordinator's login"
}

coord_advertise() {
  step "Announcing the coordinator on the network"
  mkdir -p /etc/avahi/services
  sed "s/@API_PORT@/$HTTP_PORT/" "$BUNDLE_DIR/showmesh.service" > /etc/avahi/services/showmesh.service
  chmod 0644 /etc/avahi/services/showmesh.service
  if have_systemd; then
    systemctl enable --now avahi-daemon >/dev/null 2>&1 || warn "avahi-daemon did not start, so nodes must be given this coordinator's address by hand."
  fi
  ok "announced as _showmesh._tcp on port $HTTP_PORT"
}

coord_state_save() {
  env_set "$INSTALLER_STATE" 0600 "ROLE=$ROLE" "VERSION=$SHOWMESH_VERSION" "COORDINATOR_URL=$PUBLIC_URL"
}

run_coordinator_role() {
  coord_install_docker
  coord_install_bundle
  coord_choose_broker
  coord_write_env
  coord_state_save
  [ "$BROKER_MODE" = "builtin" ] && coord_builtin_credentials
  coord_start
  coord_admin
  coord_check_broker
  coord_advertise
}

coord_success_lines() {
  local addr="${PUBLIC_URL#http://}"
  addr="${addr%:*}"
  info "The coordinator $SHOWMESH_VERSION is running and its broker accepts logins."
  info "Operator UI: http://$addr:$UI_PORT"
  if [ "${ADMIN_READY:-0}" -eq 1 ]; then
    info "Next, enroll a node. Here, run: sudo showmeshctl node enroll porch-left"
    info "Then run the install command it prints on that node."
  fi
}
