# shellcheck shell=bash
# Shared process and container teardown for the spike drivers.
# spike/m1/run.sh through spike/m5/run.sh source this file. It has no
# top-level side effects. m6 does not source it (T76b).
#
# A long step has to run under spike_fg (or spike_spawn plus wait). bash
# runs a trap only after the current foreground command returns, and wait
# returns as soon as a trapped signal arrives. set -m is enabled only
# around the background start, so the child gets its own process group
# and the driver's group still receives the terminal's Ctrl-C.
#
# Callers:
#   spike_run_init <milestone>
#   spike_spawn [--class stack|transient] [--out path] [--err path] [--append] <name> [--out path] [--err path] [--append] <cmd...>
# Redirect flags may sit before the name or between the name and the
# command. The command's own flags are not scanned.
#   spike_fg <cmd...>
#   spike_teardown_begin <EXIT|INT|TERM>   # hook, containers, process groups
#   spike_teardown_finish <status>
#   spike_install_trap                     # tests and drivers with no extra steps
#   spike_emit_children                    # shell assignments for stack.env
#   spike_load_children_from_env
#   spike_docker                           # docker run plus the run label, when set
#
# SPIKE_STOP_GRACE (default 5) is the one shared deadline in seconds.
# KEEP_UP=1 leaves class "stack" processes alive and still stops transient
# ones and labelled containers. spike_cleanup_pre, when the caller defines
# it, runs once before containers and processes.

# shellcheck disable=SC2317,SC2329 # invoked by the EXIT/INT/TERM trap
spike_trap_cleanup() {
  local code=$?
  local why="${1:-EXIT}"
  spike_teardown_begin "$why" || return 0
  spike_teardown_finish "$code"
}

spike_install_trap() {
  trap 'spike_trap_cleanup EXIT' EXIT
  trap 'spike_trap_cleanup INT' INT
  trap 'spike_trap_cleanup TERM' TERM
}

spike_run_init() {
  local milestone="$1" id=""
  if [[ ! "$milestone" =~ ^[a-z0-9-]+$ ]]; then
    echo "spike_run_init: bad milestone" >&2
    return 1
  fi
  id="$(openssl rand -hex 4)"
  if [[ ! "$id" =~ ^[0-9a-f]{8}$ ]]; then
    echo "spike_run_init: bad run id" >&2
    return 1
  fi
  SPIKE_RUN_ID="$id"
  SPIKE_RUN_LABEL="${milestone}-${id}"
  if [[ ! "$SPIKE_RUN_LABEL" =~ ^[a-z0-9-]+$ ]]; then
    echo "spike_run_init: bad run label" >&2
    return 1
  fi
  if [[ -z "${SPIKE_REG:-}" ]]; then
    if [[ -n "${WORK:-}" ]]; then
      SPIKE_REG="${WORK}/spike-run"
    else
      SPIKE_REG="$(mktemp -d)"
    fi
  fi
  rm -rf "$SPIKE_REG"
  mkdir -p "$SPIKE_REG/children"
  chmod 700 "$SPIKE_REG" || true
  printf '%s\n' 0 >"$SPIKE_REG/seq"
  export SPIKE_RUN_ID SPIKE_RUN_LABEL SPIKE_REG
}

spike_ensure_reg() {
  if [[ -n "${SPIKE_REG:-}" && -d "${SPIKE_REG}/children" ]]; then
    return 0
  fi
  spike_run_init "${SPIKE_MILESTONE:-spike}"
}

spike_our_pgid() {
  ps -o pgid= -p $$ 2>/dev/null | tr -d '[:space:]'
}

spike_read_field() {
  local path="$1"
  if [[ -f "$path" ]]; then
    cat "$path"
  fi
}

# spike_spawn records pid, pgid, and ps identity so a recycled pid is not
# signalled. Stdout and stderr stay with the caller unless --out or --err
# is set, which is how drivers keep their log paths.
spike_spawn() {
  local class="stack" out="" err="" append=0 name=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --class)
        class="$2"
        shift 2
        ;;
      --out)
        out="$2"
        shift 2
        ;;
      --err)
        err="$2"
        shift 2
        ;;
      --append)
        append=1
        shift
        ;;
      --)
        shift
        break
        ;;
      *)
        break
        ;;
    esac
  done
  if [[ "$class" != stack && "$class" != transient ]]; then
    echo "spike_spawn: class must be stack or transient" >&2
    return 1
  fi
  name="${1:-}"
  shift || true
  if [[ -z "$name" ]]; then
    echo "spike_spawn: name and command are required" >&2
    return 1
  fi
  if [[ ! "$name" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "spike_spawn: bad name" >&2
    return 1
  fi
  # Drivers write: spike_spawn --class stack <name> --out <path> --err <path> <cmd...>
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --out)
        if [[ $# -lt 2 ]]; then
          echo "spike_spawn: --out needs a path" >&2
          return 1
        fi
        out="$2"
        shift 2
        ;;
      --err)
        if [[ $# -lt 2 ]]; then
          echo "spike_spawn: --err needs a path" >&2
          return 1
        fi
        err="$2"
        shift 2
        ;;
      --append)
        append=1
        shift
        ;;
      --)
        shift
        break
        ;;
      *)
        break
        ;;
    esac
  done
  if [[ $# -eq 0 ]]; then
    echo "spike_spawn: name and command are required" >&2
    return 1
  fi
  spike_ensure_reg
  local monitor=0
  case $- in
    *m*) monitor=1 ;;
  esac
  set -m
  # Same path for both streams uses one open (the >file 2>&1 form).
  # Different paths stay split. --out alone leaves stderr with the caller.
  if [[ -n "$out" && -n "$err" && "$out" == "$err" ]]; then
    if [[ "$append" -eq 1 ]]; then
      "$@" >>"$out" 2>&1 &
    else
      "$@" >"$out" 2>&1 &
    fi
  elif [[ -n "$out" && -n "$err" ]]; then
    if [[ "$append" -eq 1 ]]; then
      "$@" >>"$out" 2>>"$err" &
    else
      "$@" >"$out" 2>"$err" &
    fi
  elif [[ -n "$out" ]]; then
    if [[ "$append" -eq 1 ]]; then
      "$@" >>"$out" &
    else
      "$@" >"$out" &
    fi
  elif [[ -n "$err" ]]; then
    if [[ "$append" -eq 1 ]]; then
      "$@" 2>>"$err" &
    else
      "$@" 2>"$err" &
    fi
  else
    "$@" &
  fi
  local pid=$!
  if [[ "$monitor" -eq 0 ]]; then
    set +m
  fi
  local pgid="" lstart="" args="" quoted="" part="" seq="" dir=""
  pgid="$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d '[:space:]' || true)"
  lstart="$(ps -o lstart= -p "$pid" 2>/dev/null || true)"
  args="$(ps -o args= -p "$pid" 2>/dev/null || true)"
  if [[ -z "$pgid" ]]; then
    pgid="$pid"
  fi
  quoted=""
  for part in "$@"; do
    quoted="${quoted}$(printf '%q' "$part") "
  done
  seq="$(spike_read_field "$SPIKE_REG/seq")"
  seq=$((seq + 1))
  printf '%s\n' "$seq" >"$SPIKE_REG/seq"
  dir=$(printf '%s/children/%04d' "$SPIKE_REG" "$seq")
  mkdir -p "$dir"
  printf '%s\n' "$class" >"$dir/class"
  printf '%s\n' "$name" >"$dir/name"
  printf '%s\n' "$pid" >"$dir/pid"
  printf '%s\n' "$pgid" >"$dir/pgid"
  printf '%s\n' "$lstart" >"$dir/lstart"
  printf '%s\n' "$args" >"$dir/args"
  printf '%s\n' "$quoted" >"$dir/argv"
  printf '%s\n' "$out" >"$dir/out"
  printf '%s\n' "$err" >"$dir/err"
  printf '%s\n' "$append" >"$dir/append"
  SPIKE_LAST_PID="$pid"
  # Callers (run_bounded, specs restart) read these after spike_spawn.
  # shellcheck disable=SC2034
  SPIKE_LAST_PGID="$pgid"
  # shellcheck disable=SC2034
  SPIKE_LAST_DIR="$dir"
}

# spike_fg runs a long foreground step as a child and waits for it, so a
# trapped INT or TERM is handled within about a second. The status is the
# child's status.
spike_fg() {
  local rc=0
  spike_spawn --class transient fg "$@"
  wait "$SPIKE_LAST_PID" || rc=$?
  return "$rc"
}

# spike_fg_dir runs a command in another directory. The bash -c process
# stays the group leader (no exec), so the recorded argv still matches
# when the trap signals the group.
spike_fg_dir() {
  local dir="$1"
  shift
  # shellcheck disable=SC2016 # single quotes are the child script
  spike_fg bash -c 'cd "$1" && shift && "$@"' spike-fg-dir "$dir" "$@"
}

spike_pid_identity_ok() {
  local dir="$1" pid="" want_lstart="" want_args="" got_lstart="" got_args=""
  pid="$(spike_read_field "$dir/pid")"
  if [[ -z "$pid" ]] || ! kill -0 "$pid" 2>/dev/null; then
    return 1
  fi
  want_lstart="$(spike_read_field "$dir/lstart")"
  want_args="$(spike_read_field "$dir/args")"
  got_lstart="$(ps -o lstart= -p "$pid" 2>/dev/null || true)"
  got_args="$(ps -o args= -p "$pid" 2>/dev/null || true)"
  [[ "$got_lstart" == "$want_lstart" && "$got_args" == "$want_args" ]]
}

spike_signal_group() {
  local sig="$1" pgid="$2" ours=""
  if [[ -z "$pgid" || ! "$pgid" =~ ^[0-9]+$ ]]; then
    return 0
  fi
  ours="$(spike_our_pgid)"
  if [[ -n "$ours" && "$pgid" == "$ours" ]]; then
    return 0
  fi
  kill "-${sig}" -- -"$pgid" 2>/dev/null || true
}

spike_stop_processes() {
  local grace="${SPIKE_STOP_GRACE:-5}" dir="" pid="" pgid="" class=""
  local list="" start="" now="" alive=0 g=""
  [[ -n "${SPIKE_REG:-}" && -d "${SPIKE_REG}/children" ]] || return 0
  list="${SPIKE_REG}/stop-pgids"
  : >"$list"
  for dir in "$SPIKE_REG"/children/*; do
    [[ -d "$dir" ]] || continue
    class="$(spike_read_field "$dir/class")"
    if [[ "${KEEP_UP:-}" == 1 && "$class" == stack ]]; then
      continue
    fi
    pid="$(spike_read_field "$dir/pid")"
    pgid="$(spike_read_field "$dir/pgid")"
    if kill -0 "$pid" 2>/dev/null; then
      if ! spike_pid_identity_ok "$dir"; then
        continue
      fi
    fi
    printf '%s\n' "$pgid" >>"$list"
    spike_signal_group TERM "$pgid"
  done
  start="$(date +%s)"
  while true; do
    alive=0
    while IFS= read -r g || [[ -n "$g" ]]; do
      [[ -z "$g" ]] && continue
      if kill -0 -- -"$g" 2>/dev/null; then
        alive=1
      fi
    done <"$list"
    if [[ "$alive" -eq 0 ]]; then
      break
    fi
    now="$(date +%s)"
    if [[ $((now - start)) -ge "$grace" ]]; then
      break
    fi
    sleep 0.2
  done
  # bash prints "Killed <cmd>" when it notices a job died from SIGKILL.
  # That note is not a driver error. Keep it off the caller's stderr.
  {
    while IFS= read -r g || [[ -n "$g" ]]; do
      [[ -z "$g" ]] && continue
      spike_signal_group KILL "$g"
    done <"$list"
    for dir in "$SPIKE_REG"/children/*; do
      [[ -d "$dir" ]] || continue
      class="$(spike_read_field "$dir/class")"
      if [[ "${KEEP_UP:-}" == 1 && "$class" == stack ]]; then
        continue
      fi
      pid="$(spike_read_field "$dir/pid")"
      if kill -0 "$pid" 2>/dev/null && ! spike_pid_identity_ok "$dir"; then
        continue
      fi
      disown "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    done
  } 2>/dev/null
}

spike_stop_containers() {
  local grace="${SPIKE_STOP_GRACE:-5}" ids="" id=""
  local -a arr=()
  if [[ -z "${SPIKE_RUN_LABEL:-}" ]]; then
    return 0
  fi
  if [[ ! "$SPIKE_RUN_LABEL" =~ ^[a-z0-9-]+$ ]]; then
    return 0
  fi
  command -v docker >/dev/null 2>&1 || return 0
  ids="$(docker ps -aq --filter "label=spike-run=${SPIKE_RUN_LABEL}" 2>/dev/null || true)"
  if [[ -z "$ids" ]]; then
    return 0
  fi
  for id in $ids; do
    arr+=("$id")
  done
  docker stop -t "$grace" "${arr[@]}" >/dev/null 2>&1 || true
  docker rm -f "${arr[@]}" >/dev/null 2>&1 || true
}

# spike_teardown_begin is idempotent. A second signal, or the EXIT that
# follows TERM, returns 1 and the caller must not repeat the hook.
spike_teardown_begin() {
  local why="${1:-EXIT}"
  if [[ "${SPIKE_TEARDOWN_DONE:-}" == 1 ]]; then
    return 1
  fi
  SPIKE_TEARDOWN_DONE=1
  SPIKE_STOP_WHY="$why"
  trap - EXIT INT TERM ERR
  # A second TERM must not abort the teardown halfway through.
  trap '' INT TERM
  set +e
  if declare -F spike_cleanup_pre >/dev/null 2>&1; then
    spike_cleanup_pre || true
  fi
  spike_stop_containers || true
  spike_stop_processes || true
  return 0
}

spike_teardown_finish() {
  local code="${1:-0}"
  case "${SPIKE_STOP_WHY:-EXIT}" in
    INT) exit 130 ;;
    TERM) exit 143 ;;
    *) exit "$code" ;;
  esac
}

# spike_emit_children prints stack-class records as shell assignments.
# Values are %q-quoted. SPIKE_CHILD_N_ARGV is a list of %q words.
spike_emit_children() {
  local dir="" n=0 class=""
  if [[ -z "${SPIKE_REG:-}" || ! -d "${SPIKE_REG}/children" ]]; then
    printf 'SPIKE_CHILD_COUNT=%q\n' 0
    return 0
  fi
  for dir in "$SPIKE_REG"/children/*; do
    [[ -d "$dir" ]] || continue
    class="$(spike_read_field "$dir/class")"
    if [[ "$class" != stack ]]; then
      continue
    fi
    n=$((n + 1))
    printf 'SPIKE_CHILD_%s_CLASS=%q\n' "$n" "$class"
    printf 'SPIKE_CHILD_%s_NAME=%q\n' "$n" "$(spike_read_field "$dir/name")"
    printf 'SPIKE_CHILD_%s_PID=%q\n' "$n" "$(spike_read_field "$dir/pid")"
    printf 'SPIKE_CHILD_%s_PGID=%q\n' "$n" "$(spike_read_field "$dir/pgid")"
    printf 'SPIKE_CHILD_%s_LSTART=%q\n' "$n" "$(spike_read_field "$dir/lstart")"
    printf 'SPIKE_CHILD_%s_ARGS=%q\n' "$n" "$(spike_read_field "$dir/args")"
    printf 'SPIKE_CHILD_%s_ARGV=%q\n' "$n" "$(spike_read_field "$dir/argv")"
    printf 'SPIKE_CHILD_%s_OUT=%q\n' "$n" "$(spike_read_field "$dir/out")"
    printf 'SPIKE_CHILD_%s_ERR=%q\n' "$n" "$(spike_read_field "$dir/err")"
    printf 'SPIKE_CHILD_%s_APPEND=%q\n' "$n" "$(spike_read_field "$dir/append")"
  done
  printf 'SPIKE_CHILD_COUNT=%q\n' "$n"
}

spike_load_children_from_env() {
  local i=1 n=0 dir="" value="" field=""
  n="${SPIKE_CHILD_COUNT:-0}"
  if [[ -z "${SPIKE_REG:-}" ]]; then
    SPIKE_REG="$(mktemp -d)"
  fi
  rm -rf "$SPIKE_REG/children"
  mkdir -p "$SPIKE_REG/children"
  printf '%s\n' "$n" >"$SPIKE_REG/seq"
  while [[ "$i" -le "$n" ]]; do
    dir=$(printf '%s/children/%04d' "$SPIKE_REG" "$i")
    mkdir -p "$dir"
    for field in class name pid pgid lstart args argv out err append; do
      # Quoted so lstart and argv, which contain spaces, stay one value.
      eval "value=\"\${SPIKE_CHILD_${i}_$(printf '%s' "$field" | tr '[:lower:]' '[:upper:]')}\""
      printf '%s\n' "$value" >"$dir/$field"
    done
    i=$((i + 1))
  done
}

# spike_docker inserts the run label on docker run when spike_run_init has
# set one. Every other docker subcommand is passed through unchanged.
spike_docker() {
  if [[ "${1:-}" == run && -n "${SPIKE_RUN_LABEL:-}" ]]; then
    shift
    command docker run --label "spike-run=${SPIKE_RUN_LABEL}" "$@"
    return
  fi
  command docker "$@"
}
