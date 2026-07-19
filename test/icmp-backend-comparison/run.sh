#!/usr/bin/env bash
#
# Manual comparison harness for the two ICMP prober backends: the default
# "native" (raw / unprivileged-fallback) prober vs the non-privileged
# "icmpengine" backend. It runs both side by side against the same targets and
# reports success rates and RTT distributions.
#
# This is NOT part of `go test` / CI — it is a manual harness for observing the
# backends against real network targets and documenting how the icmpengine
# backend was validated for parity.
#
# Prerequisites:
#   - Non-privileged ICMP must be permitted. On Linux your gid must be within
#     net.ipv4.ping_group_range:
#       sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
#   - No elevated capabilities are required (that is the point) — run as a
#     normal user, no CAP_NET_RAW.
#   - python3 (stdlib only) for the analysis.
#
# Usage:
#   ./run.sh                                  # 8.8.8.8 1.1.1.1, 600s, 5s interval
#   DURATION=120 INTERVAL=2 ./run.sh 8.8.8.8 9.9.9.9
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

PORT="${PORT:-9668}"
DURATION="${DURATION:-600}"
INTERVAL="${INTERVAL:-5}"
if [ "$#" -gt 0 ]; then TARGETS=("$@"); else TARGETS=(8.8.8.8 1.1.1.1); fi
MODULES=(icmp_native icmp_engine)
OUT="$SCRIPT_DIR/out.csv"
BIN="$SCRIPT_DIR/blackbox_exporter"

echo "Building blackbox_exporter..."
( cd "$REPO_ROOT" && go build -o "$BIN" . )

"$BIN" --config.file="$SCRIPT_DIR/compare.yml" --web.listen-address=":$PORT" \
  >"$SCRIPT_DIR/exporter.log" 2>&1 &
BBE=$!
trap 'kill "$BBE" 2>/dev/null || true' EXIT
for _ in $(seq 1 40); do
  curl -sf "localhost:$PORT/-/healthy" >/dev/null 2>&1 && break
  sleep 0.25
done

echo "round,ts,module,target,success,rtt_s,probe_duration_s" > "$OUT"
echo "Observing [${MODULES[*]}] x [${TARGETS[*]}] for ${DURATION}s (interval ${INTERVAL}s)..."
START=$(date +%s)
END=$(( START + DURATION ))
round=0
while [ "$(date +%s)" -lt "$END" ]; do
  round=$(( round + 1 ))
  for m in "${MODULES[@]}"; do
    for t in "${TARGETS[@]}"; do
      out="$(curl -s --max-time 8 "localhost:$PORT/probe?target=$t&module=$m")"
      succ="$(awk '/^probe_success /{print $2}' <<<"$out")"
      rtt="$(awk '/phase="rtt"/{print $2}' <<<"$out")"
      pdur="$(awk '/^probe_duration_seconds /{print $2}' <<<"$out")"
      echo "$round,$(date +%s),$m,$t,${succ:-NA},${rtt:-NA},${pdur:-NA}" >> "$OUT"
    done
  done
  echo -ne "  round $round, $(( $(date +%s) - START ))s elapsed\r"
  sleep "$INTERVAL"
done
echo

python3 "$SCRIPT_DIR/analyze.py" "$OUT"
