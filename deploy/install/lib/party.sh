# shellcheck shell=bash disable=SC2059,SC2155
# Decorative output for --party only: the opening banner, the success screen and the hidden modes.
# Nothing here runs before or during a refusal, an error or a prompt, and nothing here runs without --party.
# Stock macOS bash 3.2 rejects fractional read -t and negative substring offsets; this file avoids both.

PARTY_FAST="${PARTY_FAST:-0}"
PARTY_LOUD="${PARTY_LOUD:-0}"

party_colors() {
  if [ -t 1 ]; then
    R=$'\e[31m'; G=$'\e[32m'; Y=$'\e[33m'; B=$'\e[34m'; M=$'\e[35m'; C=$'\e[36m'
    W=$'\e[97m'; DIM=$'\e[2m'; BOLD=$'\e[1m'; BLINK=$'\e[5m'; X=$'\e[0m'
    HIDE=$'\e[?25l'; SHOW=$'\e[?25h'
  else
    R=; G=; Y=; B=; M=; C=; W=; DIM=; BOLD=; BLINK=; X=; HIDE=; SHOW=
  fi
  LIGHTS=("$R" "$Y" "$G" "$C" "$B" "$M")
  COLS=$( (tput cols 2>/dev/null) || echo 80 ); [ -z "$COLS" ] && COLS=80
  LINES=$( (tput lines 2>/dev/null) || echo 24 ); [ -z "$LINES" ] && LINES=24
  trap party_cleanup EXIT
}

nap()    { [ "$PARTY_FAST" -eq 1 ] && return; sleep "${1:-0.4}"; }
beep()   { [ "$PARTY_LOUD" -eq 1 ] && printf '\a'; return 0; }
party_cleanup(){ printf '%s%s' "$SHOW" "$X"; [ -t 0 ] && stty echo icanon 2>/dev/null; return 0; }

# Non-blocking Konami listener. Returns 0 if the code is entered within
# $1 seconds. Guarded on a real tty so a piped/CI run never blocks.
# Arrows arrive as ESC [ A/B/C/D; b and a are literal.
# bash 3.2 (stock on macOS) rejects fractional `read -t`, but integer `read -t`
# works and returns the instant a key is buffered. We accept both arrow
# encodings (ESC [ A and app-cursor ESC O A) and a typed "showtime" fallback.
konami_listen() {
  [ -t 0 ] || return 1
  local want="UUDDLRLRba" got="" word="" key seq c secs="${1:-5}" saved now i
  saved=$(stty -g 2>/dev/null)
  printf '\e[?1l'                    # ask for normal cursor-key mode
  local deadline=$(( $(date +%s) + secs ))
  while now=$(date +%s); [ "$now" -le "$deadline" ]; do
    printf "\r  ${DIM}listening for the code... %ds  ${X}" "$(( deadline - now ))"
    IFS= read -rsn1 -t 1 key 2>/dev/null || continue
    if [ "$key" = $'\e' ]; then
      # slurp the rest of the escape sequence until its final letter, so
      # ESC[A, ESC O A and modified forms like ESC[1;5A all classify by
      # that last A/B/C/D. Length is never assumed.
      seq=""
      for ((i=0; i<8; i++)); do
        IFS= read -rsn1 -t 1 c 2>/dev/null || break
        seq+="$c"
        case "$c" in
          O) : ;;                 # SS3 introducer (ESC O A), keep reading
          [A-Za-z~]) break ;;     # final byte of the sequence
        esac
      done
      [ -n "${SHOWMESH_DEBUG_KEYS:-}" ] && printf '\n  [debug] arrow seq = %q\n' "$seq" >&2
      case "$seq" in *A) got+=U ;; *B) got+=D ;; *C) got+=R ;; *D) got+=L ;; esac
    else
      case "$key" in b|B) got+=b ;; a|A) got+=a ;; esac
      word+="$key"
      [ -n "${SHOWMESH_DEBUG_KEYS:-}" ] && printf '\n  [debug] key = %q\n' "$key" >&2
    fi
    case "$got"  in *"$want")  printf "\r%*s\r" 44 ""; stty "$saved" 2>/dev/null; return 0 ;; esac
    case "$word" in *showtime) printf "\r%*s\r" 44 ""; stty "$saved" 2>/dev/null; return 0 ;; esac
  done
  printf "\r%*s\r" 44 ""; stty "$saved" 2>/dev/null; return 1
}

# ---- typewriter ----------------------------------------------------------
type_out() {
  local s="$1" col="${2:-$W}" i ch
  if [ "$PARTY_FAST" -eq 1 ]; then printf '%s%s%s\n' "$col" "$s" "$X"; return; fi
  printf '%s' "$col"
  for ((i=0; i<${#s}; i++)); do ch="${s:$i:1}"; printf '%s' "$ch"; sleep 0.012; done
  printf '%s\n' "$X"
}

# ---- a chasing pixel string ---------------------------------------------
chase() {
  local n="${1:-24}" passes="${2:-2}" i p pos
  printf '%s' "$HIDE"
  for ((p=0; p<passes; p++)); do
    for ((pos=0; pos<n; pos++)); do
      printf '\r  '
      for ((i=0; i<n; i++)); do
        if [ "$i" -eq "$pos" ]; then printf '%s█%s' "${LIGHTS[$(((i+p)%${#LIGHTS[@]}))]}" "$X"
        else printf '%s·%s' "$DIM" "$X"; fi
      done
      nap 0.02
    done
  done
  printf '\r  '; for ((i=0;i<n;i++)); do printf '%s█%s' "${LIGHTS[$((i%${#LIGHTS[@]}))]}" "$X"; done
  printf '%s\n' "$SHOW"
}


p_check() { printf "  ${G}✔${X} %s\n" "$1"; nap 0.2; }
p_warn()  { printf "  ${Y}⚠${X} %s\n" "$1"; nap 0.2; }
p_step()  { printf "\n${C}${BOLD}▸ %s${X}\n" "$1"; nap 0.2; }

banner() {
  local cyc="${1:-0}"
  local -a lines=(
"   ███████╗██╗  ██╗ ██████╗ ██╗    ██╗|███╗   ███╗███████╗███████╗██╗  ██╗"
"   ██╔════╝██║  ██║██╔═══██╗██║    ██║|████╗ ████║██╔════╝██╔════╝██║  ██║"
"   ███████╗███████║██║   ██║██║ █╗ ██║|██╔████╔██║█████╗  ███████╗███████║"
"   ╚════██║██╔══██║██║   ██║██║███╗██║|██║╚██╔╝██║██╔══╝  ╚════██║██╔══██║"
"   ███████║██║  ██║╚██████╔╝╚███╔███╔╝|██║ ╚═╝ ██║███████╗███████║██║  ██║"
"   ╚══════╝╚═╝  ╚═╝ ╚═════╝  ╚══╝╚══╝ |╚═╝     ╚═╝╚══════╝╚══════╝╚═╝  ╚═╝")
  local idx=0 l left right
  for l in "${lines[@]}"; do
    left="${l%%|*}"; right="${l#*|}"
    printf '%s%s%s%s%s%s\n' \
      "${LIGHTS[$(((idx+cyc)%6))]}" "$BOLD" "$left" \
      "${LIGHTS[$(((idx+cyc+3)%6))]}" "$right" "$X"
    idx=$((idx+1))
  done
}

fortune() {
  local -a t=(
"tip: the show continues even when the coordinator does not."
"tip: desired state and observed state are not the same thing. respect the gap."
"tip: an e-stop that only stops playout is not an e-stop."
"tip: if it is not on the timing path, it cannot break the timing path."
"tip: a node that cannot say which build it runs is not a safe fallback."
"tip: photons are expensive. test on the bench first.")
  printf "  ${DIM}%s${X}\n" "${t[$((RANDOM % ${#t[@]}))]}"
}

egg_disco() {
  local n=$(( COLS>60 ? 52 : COLS-8 )) frames=90 i f
  printf '%s' "$HIDE"
  printf '  %s%shouselights down.%s\n\n' "$Y" "$BOLD" "$X"; nap 0.4
  for ((f=0; f<frames; f++)); do
    printf '\r  '
    # every 8th frame, full-white strobe
    if (( f % 8 == 0 )); then
      beep
      for ((i=0;i<n;i++)); do printf '%s█%s' "$W" "$X"; done
    else
      for ((i=0;i<n;i++)); do printf '%s█%s' "${LIGHTS[$(((i+f)%6))]}" "$X"; done
    fi
    sleep 0.05
  done
  printf '\n\n  %s%sthat is the one. ship it.%s\n\n' "$M" "$BOLD" "$X"
  printf '%s' "$SHOW"
}

egg_rave() {  # like disco but with a fake BPM readout and a bass kick
  local n=$(( COLS>60 ? 52 : COLS-8 )) beats=48 i b bpm=128
  printf '%s' "$HIDE"
  printf '  %s%s♫ %d BPM ♫%s   the drop is coming\n\n' "$C" "$BOLD" "$bpm" "$X"; nap 0.5
  for ((b=0; b<beats; b++)); do
    local kick=$(( b % 4 == 0 ))
    (( kick )) && beep
    printf '\r  '
    for ((i=0;i<n;i++)); do
      if (( kick )); then printf '%s█%s' "$W" "$X"
      else printf '%s%s%s' "${LIGHTS[$(((i*b)%6))]}" "$( (( (i+b)%3==0 )) && echo '█' || echo '░')" "$X"; fi
    done
    (( kick )) && printf ' %s♥%s' "$R" "$X"
    sleep 0.06
  done
  printf '\n\n  %s%severybody was kung fu lighting.%s\n\n' "$M" "$BOLD" "$X"
  printf '%s' "$SHOW"
}

# The "Wake up, Neo..." opening: green text typed on a blinking cursor,
# each line cleared before the next, exactly like the film's cold open.
matrix_intro() {
  local -a msgs=("Wake up, Neo..." "The Matrix has you..." "Follow the white rabbit." "Knock, knock, Neo.")
  local msg i k line ch tspeed=0.075 bspeed=0.28
  [ "$PARTY_FAST" -eq 1 ] && { tspeed=0.02; bspeed=0.1; }
  clear 2>/dev/null || true
  printf '%s%s\n\n\n' "$HIDE" "$G"
  for msg in "${msgs[@]}"; do
    line=""
    for ((i=0; i<${#msg}; i++)); do
      ch="${msg:$i:1}"; line+="$ch"
      printf "\r  %s%s%s▊%s" "$G" "$line" "$G" "$X"
      sleep "$tspeed"
    done
    for ((k=0; k<3; k++)); do
      printf "\r  %s%s %s▊%s" "$G" "$line" "$G" "$X"; sleep "$bspeed"
      printf "\r  %s%s  %s"        "$G" "$line" "$X";       sleep "$bspeed"
    done
    printf "\r%*s\r" "$COLS" ""
    nap 0.25
  done
}

egg_matrix() {
  local w=$(( COLS>2 ? COLS : 80 )) rows=$(( LINES>4 ? LINES+6 : 28 )) r c ch
  local charset='0123456789ABCDEFｱｲｳｴｵｶｷｸｹｺ<>=*+:.'
  matrix_intro
  printf '%s%s' "$HIDE" "$G"
  clear 2>/dev/null || true
  for ((r=0; r<rows; r++)); do
    for ((c=0; c<w; c++)); do
      if (( RANDOM % 100 < 22 )); then
        ch="${charset:$((RANDOM % ${#charset})):1}"
        if (( RANDOM % 100 < 12 )); then printf '%s%s%s' "$W" "$ch" "$G"; else printf '%s' "$ch"; fi
      else printf ' '; fi
    done
    printf '\n'; sleep 0.04
  done
  printf '%s\n  %s%swake up, operator.%s\n' "$X" "$G" "$BOLD" "$X"
  printf '  %sthe show has you.%s\n\n' "$DIM$G" "$X"
  printf '%s' "$SHOW"
}

egg_karaoke() {
  local text="   ★ THE SHOW GOES ON ★ THE SHOW GOES ON ★   "
  local n=${#text}
  local win=$(( COLS>20 ? COLS-4 : 40 )) i pos ball=0 dir=1 frames=70
  printf '%s' "$HIDE"; clear 2>/dev/null || true
  printf '\n  %s%s~ now playing: greatest hits of the installer ~%s\n\n' "$M" "$BOLD" "$X"
  for ((i=0; i<frames; i++)); do
    # scrolling marquee
    local view="${text:$((i%n))}${text}"; view="${view:0:win}"
    printf "\r  %s%s%s" "$C" "$view" "$X"
    # bouncing ball line under it
    (( ball+=dir )); (( ball<=0 )) && dir=1; (( ball>=win-1 )) && dir=-1
    printf "\n  %*s%s●%s\e[A" "$ball" "" "$Y" "$X"
    sleep 0.07
  done
  printf "\n\n  %s%s*mic drop*%s\n\n" "$M" "$BOLD" "$X"
  printf '%s' "$SHOW"
}

egg_fireworks() {
  local w=$(( COLS>20 ? COLS : 60 )) h=$(( LINES>10 ? LINES-2 : 18 )) cx cy rad
  printf '%s' "$HIDE"; clear 2>/dev/null || true
  for _ in 1 2 3 4 5; do
    cx=$(( 6 + RANDOM % (w-12) )); cy=$(( 3 + RANDOM % (h-6) ))
    local col="${LIGHTS[$((RANDOM%6))]}"
    # launch
    local y
    for ((y=h; y>cy; y--)); do
      printf '\e[%d;%dH%s|%s' "$y" "$cx" "$Y" "$X"
      sleep 0.01
      printf '\e[%d;%dH ' "$y" "$cx"
    done
    # burst
    for rad in 1 2 3 4; do
      printf '\e[%d;%dH%s*%s' "$cy" "$((cx))" "$col" "$X"
      printf '\e[%d;%dH%s.%s' "$((cy-rad))" "$cx" "$col" "$X"
      printf '\e[%d;%dH%s.%s' "$((cy+rad))" "$cx" "$col" "$X"
      printf '\e[%d;%dH%s.%s' "$cy" "$((cx-rad*2))" "$col" "$X"
      printf '\e[%d;%dH%s.%s' "$cy" "$((cx+rad*2))" "$col" "$X"
      printf '\e[%d;%dH%so%s' "$((cy-rad))" "$((cx-rad))" "$col" "$X"
      printf '\e[%d;%dH%so%s' "$((cy-rad))" "$((cx+rad))" "$col" "$X"
      printf '\e[%d;%dH%so%s' "$((cy+rad))" "$((cx-rad))" "$col" "$X"
      printf '\e[%d;%dH%so%s' "$((cy+rad))" "$((cx+rad))" "$col" "$X"
      sleep 0.06
    done
    sleep 0.15
  done
  printf '\e[%d;1H%s  opening night. nice.%s\n\n' "$h" "$M" "$X"
  printf '%s' "$SHOW"
}

# Prints a themed one-liner based on the date, or SHOWMESH_SEASON if set
# (auto, halloween, xmas, newyear, july4, or a two-digit month).
seasonal_line() {
  local key="${SHOWMESH_SEASON:-auto}" mo dd m d
  case "$key" in
    auto)                 mo=$(date +%m); dd=$(date +%d) ;;
    halloween)            mo=10; dd=31 ;;
    xmas|christmas)       mo=12; dd=25 ;;
    newyear|new-year)     mo=01; dd=01 ;;
    july4|independence)   mo=07; dd=04 ;;
    valentine|valentines) mo=02; dd=14 ;;
    spring)               mo=04; dd=15 ;;
    summer)               mo=07; dd=15 ;;
    fall|autumn)          mo=11; dd=15 ;;
    winter)               mo=01; dd=15 ;;
    *)                    mo="$key"; dd=15 ;;
  esac
  m=$((10#$mo)); d=$((10#$dd))   # 10# so 08/09 are not read as bad octal
  # specific days and themed months win
  if   (( m==12 && d==25 )); then printf "  %s❄ merry christmas. the lights are already up.%s\n" "$G" "$X"; return
  elif (( m==1  && d==1  )); then printf "  %s🎉 new year, new show. who dis.%s\n" "$C" "$X"; return
  elif (( m==2  && d==14 )); then printf "  %s♥ roses are red, your gels are too.%s\n" "$R" "$X"; return
  elif (( m==7  && d==4  )); then printf "  %s🎆 real fireworks outside. fake ones in --fireworks.%s\n" "$B" "$X"; return
  elif (( m==10 ));          then printf "  %s🎃 spooky season. mind the fog machine.%s\n" "$Y" "$X"; return
  elif (( m==12 ));          then printf "  %s❄ happy holidays. the lights are already up.%s\n" "$G" "$X"; return
  fi
  # everyday ambience so the boot is never bare
  case "$m" in
    12|1|2)  printf "  %s❄ cold out there. warm under the lights.%s\n" "$C" "$X" ;;
    3|4|5)   printf "  %s🌱 spring shows are the best shows.%s\n" "$G" "$X" ;;
    6|7|8)   printf "  %s☀ festival season. hydrate, operator.%s\n" "$Y" "$X" ;;
    9|10|11) printf "  %s🍂 sweater weather and set builds.%s\n" "$Y" "$X" ;;
  esac
}

egg_alive() {
  printf '%s' "$HIDE"; clear 2>/dev/null || true
  local w=$(( COLS>44 ? COLS-6 : 40 )) i
  printf '\n  %sbringing the node to life...%s\n\n' "$DIM" "$X"; nap 0.5
  for _ in 1 2 3; do
    printf '  %s' "$G"
    for ((i=0; i<w; i++)); do
      if (( i == w/2 ));   then printf '%s╱%s' "$W" "$G"
      elif (( i == w/2+1 )); then printf '%s╲%s' "$W" "$G"
      else printf '─'; fi
    done
    printf '%s\n' "$X"; beep; sleep 0.45
  done
  nap 0.4
  printf '\n  '
  local msg="IT'S ALIVE"
  for ((i=0; i<${#msg}; i++)); do printf '%s%s%s' "${LIGHTS[$((i%6))]}$BOLD$BLINK" "${msg:$i:1}" "$X"; sleep 0.07; done
  printf '\n\n  %s%s● the node is breathing. mwahaha.%s\n\n' "$G" "$BOLD" "$X"
  printf '%s' "$SHOW"
}

egg_projector() {
  printf '%s' "$HIDE"
  local -a art=(
"    ______"
"   |  __  |\\"
"   | |__| | \\====@@@@@@@@@@@@@@@ .-----------."
"   |  __  |  ]===@@@@@@@@@@@@@@@ |  S H O W  |"
"   | |__| | /====@@@@@@@@@@@@@@@ |  M E S H  |"
"   |______|/                    '-----------'"
"    projector                      the wall")
  local f frames=36 line ch out
  for ((f=0; f<frames; f++)); do
    clear 2>/dev/null || true
    echo
    for line in "${art[@]}"; do
      out=""
      local i
      for ((i=0; i<${#line}; i++)); do
        ch="${line:$i:1}"
        if [ "$ch" = "@" ]; then
          if (( RANDOM%100 < 55 )); then
            if (( RANDOM%100 < 25 )); then out+="${W}·${X}"; else out+="${Y}·${X}"; fi
          else out+=" "; fi
        else out+="$ch"; fi
      done
      printf "  ${C}%s${X}\n" "$out"
    done
    sleep 0.07
  done
  printf '\n  %slumens: plenty. focus: sharp. it is showtime.%s\n\n' "$DIM" "$X"
  printf '%s' "$SHOW"
}

egg_konami() {
  printf '%s' "$HIDE"
  printf '\n  %s%s↑↑↓↓←→←→ B A%s\n' "$Y" "$BOLD" "$X"; nap 0.4
  local msg="CHEAT ACTIVATED" i
  printf '  '
  for ((i=0; i<${#msg}; i++)); do printf '%s%s%s' "${LIGHTS[$((i%6))]}$BOLD" "${msg:$i:1}" "$X"; sleep 0.04; done
  printf '\n\n'
  p_check "30 extra lives ........................... granted"
  p_check "unlimited fog machine .................... granted"
  p_check "the good gel colors ...................... granted"
  p_warn  "actual laws of physics ................... still enforced"
  printf '\n  %s%syou were always the grandmaster.%s\n\n' "$M" "$BOLD" "$X"
  nap 0.5
}

egg_boss() {
  printf '%s' "$HIDE"
  local n=$(( COLS>60 ? 52 : COLS-8 )) i f
  printf '  %shaving fun...%s ' "$DIM" "$X"; nap 0.4
  for ((f=0; f<18; f++)); do
    printf '\r  '; for ((i=0;i<n;i++)); do printf '%s█%s' "${LIGHTS[$(((i+f)%6))]}" "$X"; done
    sleep 0.05
  done
  # someone is coming. LOOK BUSY.
  printf '\r  %s%s⚠ someone is coming…%s' "$R" "$BOLD" "$X"; sleep 0.35
  clear 2>/dev/null || true
  cat <<'BOSS'
$ git status
On branch main
Your branch is up to date with 'origin/main'.

nothing to commit, working tree clean
$ go build ./... && echo ok
ok
$ git log --oneline -3
4bc9b1c UI: show local clock and sync status on Node Detail
bd28bee Register: mark asset.delete and clock signal shipped
6f0bcac Report each node's PTP frequency steering in ppm
$ _
BOSS
  nap 2.6
  printf '\n  %s(...coast is clear. back to the show.)%s\n\n' "$DIM" "$X"
  printf '%s' "$SHOW"
}

egg_credits() {
  clear 2>/dev/null || true
  local -a c=(
"" "" "        S H O W M E S H" "        node installer" ""
"        the show goes on." "" ""
"   built by            an operator who likes lights"
"   powered by          coffee and MQTT"
"   timing courtesy of  a PTP grandmaster somewhere"
"   e-stop by           the big red button"
"   NDI runtime         (not included, ask ADR-010)"
"   no photons          were harmed" "" ""
"   thanks for coming. mind the cables on your way out." "" "")
  local line
  for line in "${c[@]}"; do
    printf '%s%s%s\n' "$C" "$line" "$X"; nap 0.35
  done
}


# party_mode runs one hidden mode by name and returns 1 when the name is not one.
party_mode() {
  set +e
  local rc=0
  case "$1" in
    disco) egg_disco ;;
    rave) egg_rave ;;
    matrix) egg_matrix ;;
    fireworks) egg_fireworks ;;
    credits) egg_credits ;;
    karaoke) egg_karaoke ;;
    boss) egg_boss ;;
    konami) egg_konami ;;
    alive) egg_alive ;;
    projector) egg_projector ;;
    fortune) fortune ;;
    *) rc=1 ;;
  esac
  set -e
  return "$rc"
}

# party_opening is the banner shown after every check that could refuse has passed.
party_opening() {
  local role_label="$1"
  set +e
  echo
  banner 0
  echo
  type_out "          installer  $SHOWMESH_VERSION  ·  $role_label  ·  the show goes on" "$W"
  seasonal_line
  chase 34 2
  echo
  if [ "$PARTY_FAST" -eq 0 ] && [ -t 0 ] && [ "${OPT_YES:-0}" -eq 0 ]; then
    printf "  ${DIM}(psst. if you know the code, enter it now...)${X}\n"
    if konami_listen 5; then egg_konami; fi
  fi
  if [ "${SHOWMESH_NODE_ID:-}" = "grandmaster" ]; then
    printf '%s%s  ALL HAIL THE GRANDMASTER CLOCK. every node syncs to you now.%s\n' "$Y" "$BOLD" "$X"
  fi
  fortune
  set -e
}

party_success() {
  set +e
  echo
  chase 34 1
  local msg="IT'S ALIVE" i
  printf '\n  '
  for ((i = 0; i < ${#msg}; i++)); do printf '%s%s%s' "${LIGHTS[$((i % 6))]}$BOLD" "${msg:$i:1}" "$X"; nap 0.07; done
  printf '\n\n'
  printf '  %sencore:%s %s--party --disco%s  %s--party --matrix%s  %s--party --credits%s\n\n' "$DIM" "$X" "$M" "$X" "$M" "$X" "$M" "$X"
  set -e
}
