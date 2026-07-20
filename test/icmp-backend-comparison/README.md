# ICMP backend comparison harness

A manual harness that runs the two ICMP prober backends side by side against the
same real targets and compares their success rates and RTT distributions:

- `implementation: native` — the default raw / unprivileged-fallback prober.
- `implementation: icmpengine` — the non-privileged backend
  ([icmpengine](https://github.com/randomizedcoder/icmpengine)), which needs
  **no `CAP_NET_RAW`**.

It exists to document how the icmpengine backend was validated for parity. It is
**not** run by `go test` or CI — run it by hand.

## Prerequisites

- Non-privileged ICMP must be permitted. On Linux your gid must be within
  `net.ipv4.ping_group_range`:
  ```
  sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
  ```
- No elevated capabilities are required (that is the whole point) — run as a
  normal user, no `CAP_NET_RAW`, no root.
- `python3` (standard library only) for the analysis.

## Usage

```
./run.sh                                  # 8.8.8.8 & 1.1.1.1, 600s, 5s interval
DURATION=120 INTERVAL=2 ./run.sh 8.8.8.8 9.9.9.9
```

`run.sh` builds the exporter from the repo root, serves `compare.yml`, polls each
backend×target every `INTERVAL` seconds for `DURATION` seconds into `out.csv`,
then runs `analyze.py`. Environment knobs: `DURATION`, `INTERVAL`, `PORT`.

## Interpreting the output

`analyze.py` prints per-backend success rate and RTT mean/median/p90/p99/stdev,
plus a **paired** RTT difference (`icmp_engine - icmp_native`) matched by round,
which cancels out time-varying network conditions. The paired median difference
is the honest measure of whether the backends differ on latency.

## Example result (10 min, 2 targets, non-root)

```
module      target       n    ok%     mean   median      p90      p99    stdev
icmp_native 8.8.8.8     115  99.1%  12.57ms  12.51ms  14.71ms  19.78ms   2.37ms
icmp_engine 8.8.8.8     116 100.0%  12.95ms  12.31ms  15.85ms  24.23ms   4.05ms

icmp_native 1.1.1.1     116 100.0%  16.76ms  16.20ms  18.29ms  31.03ms   5.57ms
icmp_engine 1.1.1.1     116 100.0%  16.09ms  16.21ms  17.95ms  24.00ms   2.29ms

Paired (icmp_engine - icmp_native), matched by round per target:
  8.8.8.8: pairs=115 mean_diff=+0.41ms median_diff=+0.04ms stdev=4.48ms
  1.1.1.1: pairs=116 mean_diff=-0.67ms median_diff=-0.21ms stdev=5.45ms
```

The paired median differences (~0.2 ms) are far smaller than the per-sample
jitter (~5 ms stdev) and flip sign between targets: the backends are
**statistically indistinguishable on RTT**. The icmpengine backend matches the
native prober on latency while dropping the raw-socket privilege requirement.
The single native miss above was one ICMP packet drop (a 5 s read timeout), i.e.
ordinary network loss, not an implementation difference.
