#!/usr/bin/env bash
# Read-only watch for stuck work while a builder agent runs. Used by the scout (Haiku);
# it only reports, it never kills or changes anything. The planner decides what to do.
#
# Usage: scripts/stuck-watch.sh [--watch SECONDS] [--repo DIR]... [--ignore TEXT]...
#   --watch N        keep checking every 60 s for up to N seconds (default 0: one pass)
#   --repo DIR       a repo the builder is editing (repeatable). No file edited in any of them
#                    for 20+ min, while no test or build runs, = the builder looks stuck.
#                    (A builder's transcript file is NOT a liveness signal: it is not written
#                    on every step.)
#   --ignore TEXT    skip findings containing TEXT (repeatable), e.g. a container already reported
#
# Output: "OK ..." lines, or "STUCK: <what> | <evidence> | <suggested check>" lines.
# Exit 0 = nothing stuck for the whole window; exit 2 = something was stuck on two checks in a row.

set -u
WATCH=0
REPOS=()
IGNORE=()
while [ $# -gt 0 ]; do
  case "$1" in
    --watch) WATCH="$2"; shift 2 ;;
    --repo) REPOS+=("$2"); shift 2 ;;
    --agent-file) shift 2 ;;  # ignored: not a reliable signal (kept so old commands still run)
    --ignore) IGNORE+=("$2"); shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 64 ;;
  esac
done

PROC_LIMIT_MIN=15      # a test or build running longer than this is suspicious
AGENT_SILENT_MIN=20    # no file edited in the builder's repos for this long, with nothing running
DOCKER_LIMIT_MIN=45    # throwaway containers (docker run --rm) older than this were probably left behind

# etime is [[dd-]hh:]mm:ss; print whole minutes.
etime_min() {
  local t="$1" d=0 h=0 m=0
  case "$t" in *-*) d="${t%%-*}"; t="${t#*-}" ;; esac
  IFS=: read -r a b c <<<"$t"
  if [ -n "${c:-}" ]; then h=$a; m=$b; else m=$a; fi
  echo $(( 10#$d * 1440 + 10#$h * 60 + 10#$m ))
}

check_once() {
  local found=""

  # 1. Long-running tests and builds (low CPU after a long time usually means hung).
  while read -r pid etime pcpu cmd; do
    local mins; mins=$(etime_min "$etime")
    if [ "$mins" -ge "$PROC_LIMIT_MIN" ]; then
      found+="STUCK: process $pid running ${mins} min at ${pcpu}% CPU | ${cmd:0:120} | check its output; the planner decides whether to stop it"$'\n'
    fi
  done < <(ps -axo pid=,etime=,pcpu=,command= | grep -E 'vitest|go test|go-build.*\.test|bun test|bun run check|make (check|test)|vite build|astro (build|check)|tsc -b|openapi-typescript' | grep -v -E 'grep|stuck-watch')

  # 2. Dev servers: listening but not answering.
  if lsof -nP -iTCP:8080 -sTCP:LISTEN >/dev/null 2>&1; then
    curl -s -m 5 -o /dev/null http://localhost:8080/health || found+="STUCK: api on :8080 listens but /health does not answer in 5 s | lsof shows a listener | check .dev/api.log"$'\n'
  fi
  if lsof -nP -iTCP:5174 -sTCP:LISTEN >/dev/null 2>&1; then
    curl -s -m 5 -o /dev/null http://localhost:5174/ || found+="STUCK: app dev server on :5174 listens but does not answer in 5 s | lsof shows a listener | check the preview logs"$'\n'
  fi

  # 3. A builder that has gone silent. Its transcript is only written between tool
  # calls, so a long test run looks like silence: only report it when no test or
  # build process is running (a hung one is caught by check 1 after 15 minutes).
  local busy
  busy=$(ps -axo command= | grep -E 'vitest|go test|go-build.*\.test|bun test|bun run|make (check|test)|vite build|astro (build|check)|tsc -b|docker run' | grep -v -E 'grep|stuck-watch|vite --port' | head -1)
  if [ ${#REPOS[@]} -gt 0 ] && [ -z "$busy" ]; then
    local latest=0 r m
    for r in "${REPOS[@]}"; do
      m=$(find "$r" -type f -mmin -240 -not -path '*/node_modules/*' -not -path '*/.git/*' -not -path '*/dist/*' -not -path '*/.dev/*' -not -path '*/.astro/*' -not -path '*/.wrangler/*' -exec stat -f '%m' {} + 2>/dev/null | sort -rn | head -1)
      [ -n "$m" ] && [ "$m" -gt "$latest" ] && latest=$m
    done
    local idle=$(( ( $(date +%s) - ${latest:-0} ) / 60 ))
    if [ "$latest" -eq 0 ] || [ "$idle" -ge "$AGENT_SILENT_MIN" ]; then
      found+="STUCK: no file edited in ${REPOS[*]} for ${idle} min and no test or build running | the builder may be waiting on something | check the builder"$'\n'
    fi
  fi

  # 4. Throwaway containers left running: only ones started with `docker run --rm`
  # (AutoRemove), which is how tests start them. Long-lived containers from other
  # projects and the dev stack's own containers are never reported.
  if command -v docker >/dev/null 2>&1; then
    while IFS='|' read -r name created; do
      [ -z "$name" ] && continue
      [ "$(docker inspect --format '{{.HostConfig.AutoRemove}}' "$name" 2>/dev/null)" = "true" ] || continue
      local cmin; cmin=$(( ( $(date +%s) - $(date -j -f "%Y-%m-%d %H:%M:%S" "${created:0:19}" +%s 2>/dev/null || echo "$(date +%s)") ) / 60 ))
      [ "$cmin" -ge "$DOCKER_LIMIT_MIN" ] && found+="STUCK: container $name up ${cmin} min | probably a test container left behind | docker ps"$'\n'
    done < <(docker ps --format '{{.Names}}|{{.CreatedAt}}' 2>/dev/null)
  fi

  # Drop findings the planner already knows about.
  local line out=""
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    local skip=""
    for pat in ${IGNORE[@]+"${IGNORE[@]}"}; do case "$line" in *"$pat"*) skip=1 ;; esac; done
    [ -z "$skip" ] && out+="$line"$'\n'
  done <<<"$found"
  printf '%s' "$out"
}

deadline=$(( $(date +%s) + WATCH ))
previous=""
while :; do
  current=$(check_once)
  if [ -n "$current" ] && [ -n "$previous" ]; then
    # Report only what was stuck on two checks in a row, to avoid one-off blips.
    printf '%s\n' "$current"
    exit 2
  fi
  previous="$current"
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 60
done
if [ -n "$previous" ]; then
  printf 'MAYBE (seen once, not confirmed):\n%s\n' "$previous"
fi
echo "OK: nothing stuck at $(date +%H:%M)"
exit 0
