# shellcheck shell=bash
# Upgrade safety: back up the coordinator first, refuse during a night, print each node's upgrade command.

BACKUP_ROOT=/var/backups/showmesh
BACKUP_KEEP=5

# upgrade_coordinator_installed is true when a coordinator container from this installer already exists.
upgrade_coordinator_installed() {
  [ -f "$COORDINATOR_DIR/.env" ] && [ -f "$COORDINATOR_DIR/docker-compose.yml" ] || return 1
  command -v docker >/dev/null 2>&1 || return 1
  [ -n "$(compose ps -a -q coordinator 2>/dev/null | head -n 1)" ]
}

# night_end_advice STATE prints the command that ends a night in that state.
night_end_advice() {
  case "$1" in
    fading-out) echo "wait for showmeshctl night status to report stopped, then run the install command again; to upgrade anyway, add --force" ;;
    preparing) echo "showmeshctl night end-session, then run the install command again; to upgrade anyway, add --force" ;;
    *) echo "showmeshctl night power-down, or showmeshctl night end-session if that is refused, then run the install command again; to upgrade anyway, add --force" ;;
  esac
}

# night_session_state URL TOKEN sets NIGHT_STATE to the session state, or "" when the coordinator cannot say.
night_session_state() {
  NIGHT_STATE=""
  NIGHT_PROBLEM=""
  command -v jq >/dev/null 2>&1 || { NIGHT_PROBLEM="This machine has no jq command to read the answer."; return; }
  http_request GET "$1/api/v1/night/session" "" "$2"
  case "$HTTP_STATUS" in
    200) NIGHT_STATE="$(printf '%s' "$HTTP_BODY" | jq -r '.session.state // empty' 2>/dev/null)" ;;
    000) NIGHT_PROBLEM="The coordinator at $1 did not answer." ;;
    401|403) NIGHT_PROBLEM="The coordinator at $1 refused this machine's sign-in." ;;
    *) NIGHT_PROBLEM="The coordinator at $1 answered with HTTP $HTTP_STATUS." ;;
  esac
  if [ -z "$NIGHT_STATE" ] && [ -z "$NIGHT_PROBLEM" ]; then
    NIGHT_PROBLEM="The coordinator at $1 did not report a night state."
  fi
}

night_is_running() {
  case "$1" in inactive|stopped) return 1 ;; *) return 0 ;; esac
}

# upgrade_refuse_or_force MESSAGE FIX stops the install, unless --force was given.
upgrade_refuse_or_force() {
  if [ "${OPT_FORCE:-0}" -eq 1 ]; then
    warn "$1 Continuing because --force was given."
    return 0
  fi
  fail "$1" "$2"
}

upgrade_check_coordinator_night() {
  step "Checking no night is running"
  local server token
  server="$(env_get "$CTL_ENV" SHOWMESH_SERVER)"
  token="$(env_get "$CTL_ENV" SHOWMESH_CTL_TOKEN)"
  [ -n "$server" ] || server="http://127.0.0.1:$(env_get "$COORD_ENV" SHOWMESH_HTTP_PORT | grep . || echo 8080)"
  night_session_state "$server" "$token"
  if [ -n "$NIGHT_PROBLEM" ]; then
    upgrade_refuse_or_force "The installer cannot tell whether a night is running. $NIGHT_PROBLEM" "start the coordinator and run the install command again, or add --force to upgrade without checking"
    return
  fi
  if night_is_running "$NIGHT_STATE"; then
    upgrade_refuse_or_force "A night session is running (state: $NIGHT_STATE), and upgrading now would interrupt the show." "$(night_end_advice "$NIGHT_STATE")"
    return
  fi
  ok "no night session is running"
}

upgrade_check_node_night() {
  [ -n "${COORDINATOR_URL:-}" ] && [ -n "${API_TOKEN:-}" ] || {
    warn "This node has no coordinator address or token saved, so the installer cannot check whether a night is running."
    return 0
  }
  night_session_state "$COORDINATOR_URL" "$API_TOKEN"
  if [ -n "$NIGHT_PROBLEM" ]; then
    warn "The installer could not check whether a night is running. $NIGHT_PROBLEM Continuing with the upgrade."
    return 0
  fi
  if night_is_running "$NIGHT_STATE"; then
    upgrade_refuse_or_force "A night session is running (state: $NIGHT_STATE), and restarting this node now would interrupt the show." "on the coordinator, $(night_end_advice "$NIGHT_STATE")"
    return
  fi
  ok "no night session is running"
}

# upgrade_node_preflight runs before an enrolled node is touched.
upgrade_node_preflight() {
  node_is_enrolled || return 0
  [ "$OPT_REENROLL" -eq 0 ] || return 0
  step "Checking no night is running"
  API_TOKEN="$(env_get "$AGENT_ENV" SHOWMESH_AGENT_API_TOKEN)"
  COORDINATOR_URL="$(env_get "$INSTALLER_STATE" COORDINATOR_URL)"
  [ -z "$OPT_COORDINATOR" ] || COORDINATOR_URL="$(normalize_url "$OPT_COORDINATOR")"
  upgrade_check_node_night
}

# upgrade_copy_settings copies the env and broker files; its output is only cp's own errors.
upgrade_copy_settings() {
  cp -a "$COORD_ENV" "$1/coordinator.env" && cp -a "$COORDINATOR_DIR/mosquitto" "$1/mosquitto"
}

# upgrade_backup copies the database, settings and broker files; the coordinator is down only for the copy.
upgrade_backup() {
  step "Backing up the coordinator before upgrading it"
  local old stamp dir cid volume data_dir log files="-f docker-compose.yml -f docker-compose.published.yml"
  log="$(run_tmp)"
  old="$(env_get "$COORD_ENV" SHOWMESH_RELEASE_VERSION)"
  data_dir="$(env_get "$COORD_ENV" SHOWMESH_DATA_DIR)"
  data_dir="${data_dir:-/var/lib/showmesh}"
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  dir="$BACKUP_ROOT/$stamp-${old:-unknown}"
  install -d -m 0700 "$BACKUP_ROOT"
  install -d -m 0700 "$dir"
  cid="$(compose ps -a -q coordinator 2>/dev/null | head -n 1)"
  if ! run_step "stopping the coordinator to copy its database" "$log" compose stop coordinator; then
    rm -rf "$dir"
    run_step "starting the coordinator again" "$(run_tmp)" compose start coordinator || true
    fail_with "$log" "The coordinator could not be stopped to copy its database, so nothing was upgraded." "cd $COORDINATOR_DIR && docker compose $files stop coordinator"
  fi
  if ! run_step "copying the coordinator's database" "$log" docker cp -a "$cid:$data_dir/." "$dir/data"; then
    rm -rf "$dir"
    run_step "starting the coordinator again" "$(run_tmp)" compose start coordinator || true
    fail_with "$log" "The coordinator's database could not be copied, so nothing was upgraded." "docker cp $cid:$data_dir/. <backup directory>, then run the install command again"
  fi
  run_step "starting the coordinator again" "$(run_tmp)" compose start coordinator ||
    warn "The coordinator did not start again after the copy. The upgrade will start it."
  volume="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "'"$data_dir"'"}}{{.Name}}{{end}}{{end}}' "$cid" 2>/dev/null)"
  if ! run_step "copying the settings and broker files" "$log" upgrade_copy_settings "$dir"; then
    rm -rf "$dir"
    fail_with "$log" "The coordinator's settings and broker files could not be copied, so nothing was upgraded." "check free space on $BACKUP_ROOT, then run the install command again"
  fi
  if [ -z "$(ls -A "$dir/data" 2>/dev/null)" ]; then
    rm -rf "$dir"
    ok "the coordinator has no database yet, so there is nothing to back up"
    return 0
  fi
  upgrade_write_restore "$dir" "${volume:-showmesh_showmesh-data}"
  run_step "removing the oldest backups" "$(run_tmp)" upgrade_prune_backups ||
    warn "The oldest backups in $BACKUP_ROOT could not be removed. Remove all but the newest $BACKUP_KEEP by hand."
  ok "backup saved in $dir"
  info "To go back to this backup, follow the steps in $dir/RESTORE.txt:"
  sed 's/^/      /' "$dir/RESTORE.txt"
}

upgrade_write_restore() {
  local dir="$1" volume="$2" files="-f docker-compose.yml -f docker-compose.published.yml"
  cat > "$dir/RESTORE.txt" <<EOF
cd $COORDINATOR_DIR && docker compose $files down
docker run --rm -v $volume:/data -v $dir:/backup:ro --entrypoint sh eclipse-mosquitto:2.0.22 -c 'find /data -mindepth 1 -delete && cp -a /backup/data/. /data/'
cp -a $dir/coordinator.env $COORD_ENV && cp -a $dir/mosquitto/. $COORDINATOR_DIR/mosquitto/
cd $COORDINATOR_DIR && docker compose $files up -d
EOF
  chmod 0600 "$dir/RESTORE.txt"
}

# upgrade_prune_backups keeps the newest BACKUP_KEEP backup directories.
upgrade_prune_backups() {
  local old
  while IFS= read -r old; do
    [ -n "$old" ] && rm -rf "${BACKUP_ROOT:?}/$old"
  done < <(ls -1 "$BACKUP_ROOT" | sort | head -n -"$BACKUP_KEEP")
}

# upgrade_coordinator_preflight runs before the installer changes anything on an installed coordinator.
upgrade_coordinator_preflight() {
  upgrade_coordinator_installed || return 0
  upgrade_check_coordinator_night
  upgrade_backup
}

# upgrade_print_node_commands lists the command that upgrades each enrolled node to this version.
upgrade_print_node_commands() {
  [ "${ADMIN_READY:-0}" -eq 1 ] || return 0
  local token nodes id ver line lines=() current=() local_id
  token="$(env_get "$CTL_ENV" SHOWMESH_CTL_TOKEN)"
  http_request GET "http://127.0.0.1:$HTTP_PORT/api/v1/nodes" "" "$token"
  [ "$HTTP_STATUS" = "200" ] || return 0
  nodes="$(printf '%s' "$HTTP_BODY" | jq -r '.nodes[] | [.nodeId, (.agentVersion // "")] | @tsv' 2>/dev/null)" || return 0
  [ -n "$nodes" ] || return 0
  local_id="$(env_get "$AGENT_ENV" SHOWMESH_NODE_ID)"
  while IFS=$'\t' read -r id ver; do
    [ "$id" != "$local_id" ] || continue
    if [ "${ver#v}" = "$SHOWMESH_VERSION" ]; then
      current+=("$id")
    else
      lines+=("$id (${ver:-no version reported}): curl -fsSL $RELEASE_BASE/get-showmesh.sh | sudo bash -s -- --yes")
    fi
  done <<< "$nodes"
  if [ "${#lines[@]}" -gt 0 ]; then
    info "Upgrade each node to $SHOWMESH_VERSION by running its command on that node:"
    for line in "${lines[@]}"; do info "  $line"; done
  fi
  if [ "${#current[@]}" -gt 0 ]; then
    info "Already on $SHOWMESH_VERSION: ${current[*]}"
  fi
}
