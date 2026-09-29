# shellcheck shell=bash disable=SC2034
# Output, prompts, platform checks and file helpers shared by every role.

ETC_DIR=/etc/showmesh
AGENT_ENV="$ETC_DIR/agent.env"
INSTALLER_STATE="$ETC_DIR/installer.env"
COORDINATOR_DIR=/opt/showmesh/coordinator
NODE_DIR=/opt/showmesh/node

INSTALL_LOG="${SHOWMESH_INSTALL_LOG:-/var/log/showmesh-install.log}"
LOG_READY=0
SPIN_PID=""
FAIL_DETAIL=""

# ui_init decides whether this run draws the layout or plain text. Plain text is
# what a pipe, a file, NO_COLOR and TERM=dumb all get, so captured output stays readable.
ui_init() {
  UI_LAYOUT=0
  UI_COLS=80
  C_STEP=""; C_OK=""; C_WARN=""; C_OFF=""
  if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
    UI_LAYOUT=1
    C_STEP=$'\033[36m\033[1m'; C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_OFF=$'\033[0m'
    UI_COLS="$( (tput cols 2>/dev/null) || printf 80 )"
    [ -n "$UI_COLS" ] || UI_COLS=80
  fi
}
ui_init

BANNER_COLORS=($'\033[31m' $'\033[33m' $'\033[32m' $'\033[36m' $'\033[34m' $'\033[35m')

# ui_banner draws the ShowMesh name once, on a terminal wide enough to hold it.
ui_banner() {
  [ "$UI_LAYOUT" -eq 1 ] || return 0
  [ "$UI_COLS" -ge 78 ] 2>/dev/null || return 0
  local lines=(
"   ███████╗██╗  ██╗ ██████╗ ██╗    ██╗|███╗   ███╗███████╗███████╗██╗  ██╗"
"   ██╔════╝██║  ██║██╔═══██╗██║    ██║|████╗ ████║██╔════╝██╔════╝██║  ██║"
"   ███████╗███████║██║   ██║██║ █╗ ██║|██╔████╔██║█████╗  ███████╗███████║"
"   ╚════██║██╔══██║██║   ██║██║███╗██║|██║╚██╔╝██║██╔══╝  ╚════██║██╔══██║"
"   ███████║██║  ██║╚██████╔╝╚███╔███╔╝|██║ ╚═╝ ██║███████╗███████║██║  ██║"
"   ╚══════╝╚═╝  ╚═╝ ╚═════╝  ╚══╝╚══╝ |╚═╝     ╚═╝╚══════╝╚══════╝╚═╝  ╚═╝")
  local idx=0 l
  printf '\n'
  for l in "${lines[@]}"; do
    printf '%s\033[1m%s%s%s\033[0m\n' \
      "${BANNER_COLORS[$((idx % 6))]}" "${l%%|*}" "${BANNER_COLORS[$(((idx + 3) % 6))]}" "${l#*|}"
    idx=$((idx + 1))
  done
}

# redact_args prints this run's arguments with the values of secret options hidden.
redact_args() {
  local parts=() hide=0 a
  for a in "$@"; do
    if [ "$hide" -eq 1 ]; then parts+=("(hidden)"); hide=0; continue; fi
    parts+=("$a")
    case "$a" in --code|--broker-password) hide=1 ;; esac
  done
  printf '%s' "${parts[*]}"
}

# log_open starts this run's section of the install log. Enrollment codes, broker
# passwords and tokens are never written to it.
log_open() {
  if [ -L "$INSTALL_LOG" ]; then
    warn "$INSTALL_LOG is a link to another file, so this run keeps no install log. Remove the link, then run the installer again to keep one."
    return 0
  fi
  mkdir -p "$(dirname "$INSTALL_LOG")" 2>/dev/null || true
  ( umask 077 && : >> "$INSTALL_LOG" ) 2>/dev/null || return 0
  chmod 0600 "$INSTALL_LOG" 2>/dev/null || true
  chown root:root "$INSTALL_LOG" 2>/dev/null || true
  LOG_READY=1
  {
    printf '\n===== showmesh-install %s started %s\n' "$SHOWMESH_VERSION" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf '      arguments: %s\n' "$(redact_args "$@")"
  } >> "$INSTALL_LOG"
}

# log_note appends one line to the install log.
log_note() {
  [ "$LOG_READY" -eq 1 ] || return 0
  printf '%s\n' "$*" >> "$INSTALL_LOG"
}

# log_file appends a command's captured output to the install log under a label.
log_file() {
  [ "$LOG_READY" -eq 1 ] || return 0
  { printf -- '--- %s\n' "$1"; cat "$2"; printf -- '--- end %s\n' "$1"; } >> "$INSTALL_LOG"
}

# spin_start draws a spinner beside LABEL until spin_stop. It draws nothing in plain text.
# The label is cut to one line, because spin_stop clears one line.
spin_start() {
  [ "$UI_LAYOUT" -eq 1 ] || return 0
  local label="${1:0:$((UI_COLS - 6))}"
  printf '\033[?25l'
  (
    frames='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
    i=0
    while :; do
      printf '\r  %s%s%s %s' "$C_STEP" "${frames:$((i % 10)):1}" "$C_OFF" "$label"
      i=$((i + 1))
      sleep 0.12
    done
  ) &
  SPIN_PID=$!
}

# spin_stop clears the spinner line, whether the step passed, failed or was interrupted.
spin_stop() {
  [ -n "$SPIN_PID" ] || return 0
  kill "$SPIN_PID" 2>/dev/null || true
  wait "$SPIN_PID" 2>/dev/null || true
  SPIN_PID=""
  printf '\r\033[K\033[?25h'
}

step() {
  if [ "$UI_LAYOUT" -eq 1 ]; then printf '\n%s▸ %s%s\n' "$C_STEP" "$*" "$C_OFF"
  else printf '\n==> %s\n' "$*"; fi
  log_note ""
  log_note "==> $*"
}

info() {
  printf '    %s\n' "$*"
  log_note "    $*"
}

ok() {
  if [ "$UI_LAYOUT" -eq 1 ]; then printf '  %s✔%s %s\n' "$C_OK" "$C_OFF" "$*"
  else printf '    ok: %s\n' "$*"; fi
  log_note "ok: $*"
}

warn() {
  if [ "$UI_LAYOUT" -eq 1 ]; then printf '  %s⚠%s %s\n' "$C_WARN" "$C_OFF" "$*" >&2
  else printf '    warning: %s\n' "$*" >&2; fi
  log_note "warning: $*"
}

# RUN_TMP is this run's private scratch directory, removed when the installer exits or is interrupted.
RUN_TMP="$(mktemp -d)"
ENV_SET_PENDING=""

installer_cleanup() {
  spin_stop
  rm -rf "$RUN_TMP"
  [ -z "$ENV_SET_PENDING" ] || rm -f "$ENV_SET_PENDING" "$ENV_SET_PENDING.next"
  [ "${PARTY_READY:-0}" -eq 1 ] && party_cleanup
  return 0
}
trap installer_cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# run_tmp prints a new private file under RUN_TMP.
run_tmp() {
  mktemp "$RUN_TMP/XXXXXX"
}

# run_step LABEL FILE CMD... runs CMD in this shell and the foreground, stdin from /dev/null,
# output to FILE and the install log, in both layouts. Only the spinner runs in the background,
# so Ctrl-C stops CMD itself and a function CMD can set variables.
run_step() {
  local label="$1" out="$2" rc=0
  shift 2
  spin_start "$label"
  "$@" >"$out" 2>&1 </dev/null || rc=$?
  spin_stop
  log_file "$label" "$out"
  return "$rc"
}

# step_warnings FILE shows each WARNING: line a step's own script printed, which would
# otherwise reach only the install log when the step succeeds.
step_warnings() {
  local line
  while IFS= read -r line; do
    warn "${line#*WARNING: }"
  done < <(grep -a 'WARNING: ' "$1" || true)
}

# fail prints the fact, anything FAIL_DETAIL holds, and the command that fixes it, then exits.
fail() {
  spin_stop
  printf '\nShowMesh install stopped. %s\n' "$1" >&2
  if [ -n "${FAIL_DETAIL:-}" ] && [ -s "$FAIL_DETAIL" ]; then
    printf 'It printed:\n' >&2
    tail -n 15 "$FAIL_DETAIL" | sed 's/^/  /' >&2
  fi
  if [ -n "${2:-}" ]; then
    printf 'To fix it, run: %s\n' "$2" >&2
  fi
  log_note "STOPPED: $1"
  if [ "$LOG_READY" -eq 1 ]; then
    printf 'Everything this run printed is kept in %s.\n' "$INSTALL_LOG" >&2
  fi
  exit 1
}

# fail_with FILE FACT [FIX] stops and shows the last lines the failing command printed.
fail_with() {
  FAIL_DETAIL="$1"
  fail "$2" "${3:-}"
}

# can_prompt is true when an operator can answer questions on this terminal.
can_prompt() {
  [ "${OPT_YES:-0}" -eq 0 ] && [ -t 0 ]
}

# need_answer stops an unattended run that is missing a value it would ask for.
need_answer() {
  fail "The installer needs $1 and has no terminal to ask on." "showmesh-install $2 (add it to the command you ran)"
}

# ask VAR "question" [default]
ask() {
  local __var="$1" __q="$2" __def="${3:-}" __ans
  if [ -n "$__def" ]; then
    printf '%s [%s]: ' "$__q" "$__def"
  else
    printf '%s: ' "$__q"
  fi
  IFS= read -r __ans || __ans=""
  [ -z "$__ans" ] && __ans="$__def"
  printf -v "$__var" '%s' "$__ans"
}

# ask_secret VAR "question" reads without echo.
ask_secret() {
  local __var="$1" __q="$2" __ans
  printf '%s: ' "$__q"
  IFS= read -rs __ans || __ans=""
  printf '\n'
  printf -v "$__var" '%s' "$__ans"
}

# confirm "question" default(y|n); returns 0 for yes.
confirm() {
  local q="$1" def="${2:-n}" ans hint="[y/N]"
  [ "$def" = "y" ] && hint="[Y/n]"
  printf '%s %s: ' "$q" "$hint"
  IFS= read -r ans || ans=""
  [ -z "$ans" ] && ans="$def"
  case "$ans" in y|Y|yes|YES|Yes) return 0 ;; *) return 1 ;; esac
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    fail "The installer must run as root." "sudo showmesh-install $*"
  fi
}

# detect_arch sets ARCH to amd64 or arm64 and MULTIARCH to its Debian triplet.
detect_arch() {
  local raw
  raw="$(dpkg --print-architecture 2>/dev/null || uname -m)"
  case "$raw" in
    amd64|x86_64) ARCH=amd64; MULTIARCH=x86_64-linux-gnu ;;
    arm64|aarch64) ARCH=arm64; MULTIARCH=aarch64-linux-gnu ;;
    *) fail "This machine is $raw. ShowMesh installs on amd64 and arm64 only." "the install command on an amd64 or arm64 machine running Debian 13" ;;
  esac
}

require_platform() {
  local id="" version_id="" major
  if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    id="$(. /etc/os-release && printf '%s' "${ID:-}")"
    # shellcheck disable=SC1091
    version_id="$(. /etc/os-release && printf '%s' "${VERSION_ID:-}")"
  fi
  major="${version_id%%.*}"
  if [ "$id" != "debian" ] || ! [ "${major:-0}" -ge 13 ] 2>/dev/null; then
    fail "This machine runs ${id:-an unknown system} ${version_id:-}. ShowMesh installs on Debian 13 (trixie) or newer only." "reinstall this machine with Debian 13, then run the install command again"
  fi
  detect_arch
}

have_systemd() {
  [ -d /run/systemd/system ]
}

# with_default_umask runs a command that installs system files with the usual 022 umask.
with_default_umask() {
  (umask 022 && "$@")
}

apt_get_install() {
  with_default_umask env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
}

apt_install() {
  local log
  log="$(run_tmp)"
  info "Installing packages: $*"
  if ! run_step "installing the packages" "$log" apt_get_install "$@"; then
    if ! run_step "updating the package lists" "$log" with_default_umask apt-get update ||
      ! run_step "installing the packages again" "$log" apt_get_install "$@"; then
      fail_with "$log" "Debian could not install $*." "apt-get update && apt-get install -y $*"
    fi
  fi
}

# env_get FILE KEY prints the value of KEY= in FILE, or nothing.
env_get() {
  [ -r "$1" ] || return 0
  sed -n "s/^$2=//p" "$1" | tail -n 1
}

# env_set FILE MODE KEY=VALUE... replaces or appends each key, keeping every other line.
# Values reach awk through its environment, never its arguments, so they stay out of ps and keep backslashes.
env_set() {
  local file="$1" mode="$2" tmp pair
  shift 2
  for pair in "$@"; do
    case "$pair" in *$'\n'*) fail "A setting for $file contains a line break." "showmesh-install again, typing each value on one line" ;; esac
  done
  mkdir -p "$(dirname "$file")"
  tmp="$(mktemp "$file.XXXXXX")"
  ENV_SET_PENDING="$tmp"
  [ -f "$file" ] && cat "$file" > "$tmp"
  for pair in "$@"; do
    ENV_SET_KEY="${pair%%=*}" ENV_SET_LINE="$pair" awk '
      BEGIN { k = ENVIRON["ENV_SET_KEY"]; line = ENVIRON["ENV_SET_LINE"]; done = 0 }
      index($0, k "=") == 1 { if (!done) { print line; done = 1 }; next }
      { print }
      END { if (!done) print line }' "$tmp" > "$tmp.next"
    mv "$tmp.next" "$tmp"
  done
  chmod "$mode" "$tmp"
  chown root:root "$tmp" 2>/dev/null || true
  mv "$tmp" "$file"
  ENV_SET_PENDING=""
}

# env_value_safe LABEL VALUE OPTION refuses a typed value that Compose or systemd would change when reading it back.
env_value_safe() {
  case "$2" in
    *[\$\"\'\\#[:space:]]*)
      fail "The $1 contains a \$, a quote, a backslash, a # or a space, and ShowMesh's settings files would change it." "showmesh-install $3 with a value that has none of those characters" ;;
  esac
}

# lan_address prints the address other machines use to reach this one.
lan_address() {
  local addr
  addr="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}')"
  [ -z "$addr" ] && addr="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [ -z "$addr" ] && addr="127.0.0.1"
  printf '%s' "$addr"
}

# http_request METHOD URL [BODY_FILE] [TOKEN] sets HTTP_STATUS, HTTP_BODY, HTTP_CONTENT_TYPE and HTTP_RETRY_AFTER.
http_request() {
  local method="$1" url="$2" body_file="${3:-}" token="${4:-}" out hdr
  out="$(run_tmp)"
  hdr="$(run_tmp)"
  local err
  err="$(run_tmp)"
  local args=(-sS -m 15 -X "$method" -o "$out" -D "$hdr" -w '%{http_code}')
  [ -n "$body_file" ] && args+=(-H 'Content-Type: application/json' --data-binary "@$body_file")
  if [ -n "$token" ]; then
    HTTP_STATUS="$(printf 'header = "Authorization: Bearer %s"\n' "$token" | curl "${args[@]}" -K - "$url" 2>"$err")" || HTTP_STATUS="000"
  else
    HTTP_STATUS="$(curl "${args[@]}" "$url" 2>"$err")" || HTTP_STATUS="000"
  fi
  HTTP_ERROR="$(tr -d '\r' < "$err" | sed 's/^curl: ([0-9]*) //' | tail -n 1)"
  HTTP_BODY="$(cat "$out")"
  HTTP_RETRY_AFTER="$(tr -d '\r' < "$hdr" | awk -F': *' 'tolower($1) == "retry-after" {print $2}' | tail -n 1)"
  HTTP_CONTENT_TYPE="$(tr -d '\r' < "$hdr" | awk -F': *' 'tolower($1) == "content-type" {print $2}' | tail -n 1)"
  # The answer itself can carry a broker password or a token, so only the request and its status are logged.
  case "$HTTP_STATUS" in
    2*|3*) ;;
    *) log_note "http: $method $url answered $HTTP_STATUS${HTTP_ERROR:+ ($HTTP_ERROR)}" ;;
  esac
  rm -f "$out" "$hdr" "$err"
}

# problem_detail prints an RFC 7807 detail from HTTP_BODY, or a plain fallback.
problem_detail() {
  local detail
  detail="$(printf '%s' "$HTTP_BODY" | jq -r '.detail // empty' 2>/dev/null || true)"
  if [ -z "$detail" ]; then
    detail="The coordinator answered with HTTP $HTTP_STATUS."
  fi
  printf '%s' "$detail"
}

# release_fetch NAME DEST downloads one release file.
release_fetch() {
  local log
  log="$(run_tmp)"
  if ! run_step "downloading $1" "$log" curl -fsSL --retry 3 -o "$2" "$RELEASE_BASE/$1"; then
    fail_with "$log" "The file $1 could not be downloaded from $RELEASE_BASE." "curl -fsSLO $RELEASE_BASE/$1"
  fi
}

# release_verify NAME FILE checks FILE against the release's SHA256SUMS.
release_verify() {
  local name="$1" file="$2" sums want got
  sums="$(run_tmp)"
  release_fetch SHA256SUMS "$sums"
  want="$(awk -v n="$name" '$2 == n || $2 == "*" n {print $1}' "$sums" | head -n 1)"
  if [ -z "$want" ]; then
    fail "The release's SHA256SUMS does not list $name, so it cannot be checked." "check that version $SHOWMESH_VERSION is a complete release"
  fi
  got="$(sha256sum "$file" | awk '{print $1}')"
  if [ "$want" != "$got" ]; then
    fail "The downloaded $name does not match the release's checksum." "run the install command again; if it repeats, the download is being altered on its way here"
  fi
  ok "$name matches the release checksum"
}
