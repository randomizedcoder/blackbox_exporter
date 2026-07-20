#!/usr/bin/env python3
"""Summarize an ICMP backend comparison run produced by run.sh.

Prints per-backend success rate and RTT distribution (mean/median/p90/p99/stdev)
and a paired, same-round RTT difference (icmp_engine - icmp_native) per target,
which cancels out time-varying network conditions.
"""
import csv
import math
import statistics as st
import sys

MODULES = ["icmp_native", "icmp_engine"]


def pct(xs, p):
    xs = sorted(xs)
    k = (len(xs) - 1) * p / 100
    lo, hi = math.floor(k), math.ceil(k)
    return xs[int(k)] if lo == hi else xs[lo] + (xs[hi] - xs[lo]) * (k - lo)


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "out.csv"
    rows = list(csv.DictReader(open(path)))
    targets = sorted({r["target"] for r in rows})

    def rtts(m, t):
        return [float(r["rtt_s"]) * 1000 for r in rows
                if r["module"] == m and r["target"] == t
                and r["success"] == "1" and r["rtt_s"] not in ("NA", "")]

    def okrate(m, t):
        xs = [r for r in rows if r["module"] == m and r["target"] == t]
        return len(xs), sum(1 for r in xs if r["success"] == "1")

    hdr = f"{'module':11} {'target':9} {'n':>4} {'ok%':>6} {'mean':>8} {'median':>8} {'p90':>8} {'p99':>8} {'stdev':>8}"
    print(hdr)
    print("-" * len(hdr))
    for t in targets:
        for m in MODULES:
            xs = rtts(m, t)
            n, ok = okrate(m, t)
            okp = 100 * ok / max(n, 1)
            if not xs:
                print(f"{m:11} {t:9} {n:4d} {okp:5.1f}%  (no successful samples)")
                continue
            print(f"{m:11} {t:9} {n:4d} {okp:5.1f}% "
                  f"{st.mean(xs):6.2f}ms {st.median(xs):6.2f}ms "
                  f"{pct(xs, 90):6.2f}ms {pct(xs, 99):6.2f}ms {st.pstdev(xs):6.2f}ms")
        print()

    print("Paired (icmp_engine - icmp_native), matched by round per target:")
    for t in targets:
        nat = {r["round"]: float(r["rtt_s"]) * 1000 for r in rows
               if r["module"] == "icmp_native" and r["target"] == t and r["success"] == "1"}
        eng = {r["round"]: float(r["rtt_s"]) * 1000 for r in rows
               if r["module"] == "icmp_engine" and r["target"] == t and r["success"] == "1"}
        common = sorted(set(nat) & set(eng), key=int)
        if not common:
            print(f"  {t}: no paired samples")
            continue
        d = [eng[r] - nat[r] for r in common]
        print(f"  {t}: pairs={len(d)} "
              f"mean_diff={st.mean(d):+.2f}ms median_diff={st.median(d):+.2f}ms "
              f"stdev={st.pstdev(d):.2f}ms")


if __name__ == "__main__":
    main()
