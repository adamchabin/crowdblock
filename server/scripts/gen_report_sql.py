#!/usr/bin/env python3
"""Generate SQL that fills ip_reports with synthetic test reports.

The output is plain SQL for psql. It reads the existing API keys inside the
database (so create the test accounts/keys first, e.g. with loadgen or the
API), picks a random key per row and spreads reported_at over the last
--days days. IPs come from a fixed pool of --ips public IPv4 addresses, with
a skew (a few addresses get most of the reports), similar to real traffic.

Example:
    python3 server/scripts/gen_report_sql.py --rows 4000000 --ips 200000 \
        --days 7 -o /tmp/reports.sql
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f /tmp/reports.sql

Notes:
- Output is deterministic for the same --seed (setseed is called in the SQL).
- Use --days <= the retention (7) or the pruner removes the rows.
- Rows are written in chunks (one transaction per chunk), so a large run does
  not hold one huge transaction.
- Test data only: this bypasses the API validation and the rate limits.
"""

import argparse
import ipaddress
import random
import sys


def parse_sources(spec):
    """'auth:0.6,fail2ban:0.3,none:0.1' -> [(name or None, cumulative weight)]."""
    parts = []
    for item in spec.split(","):
        name, _, weight = item.strip().partition(":")
        if not name or not weight:
            raise argparse.ArgumentTypeError(f"bad source item {item!r}, want name:weight")
        parts.append((None if name == "none" else name, float(weight)))
    total = sum(w for _, w in parts)
    if total <= 0:
        raise argparse.ArgumentTypeError("source weights must sum to > 0")
    out, acc = [], 0.0
    for name, w in parts:
        acc += w / total
        out.append((name, acc))
    out[-1] = (out[-1][0], 1.0)  # guard against rounding
    return out


def public_ips(n, rng):
    """n distinct globally routable IPv4 addresses (not private, not reserved)."""
    seen = set()
    out = []
    while len(out) < n:
        a = ipaddress.IPv4Address(rng.getrandbits(32))
        if not a.is_global or a.is_private or a.is_multicast or a.is_reserved:
            continue
        if a in seen:
            continue
        seen.add(a)
        out.append(str(a))
    return out


def source_case(sources):
    """SQL CASE over the column s (uniform in [0,1)) that yields the source."""
    whens = []
    for name, upto in sources[:-1]:
        value = "NULL" if name is None else f"'{name}'"
        whens.append(f"WHEN s < {upto:.6f} THEN {value}")
    last_name = sources[-1][0]
    last = "NULL" if last_name is None else f"'{last_name}'"
    return "CASE " + " ".join(whens) + f" ELSE {last} END"


def write_chunk(out, pool_sql, rows, days, sources, skew):
    out.write("BEGIN;\n")
    out.write(
        "WITH pool AS (SELECT ARRAY[" + pool_sql + "]::inet[] AS ips),\n"
        "     keys AS MATERIALIZED (\n"
        "       SELECT array_agg(id ORDER BY id) AS ids,\n"
        "              array_agg(user_id ORDER BY id) AS uids\n"
        "       FROM api_keys WHERE revoked_at IS NULL)\n"
        "INSERT INTO ip_reports (ip, api_key_id, user_id, reported_at, source)\n"
        "SELECT ip, kid, uid, ts, " + source_case(sources) + "\n"
        "FROM (\n"
        "  SELECT p.ips[1 + floor(power(random(), " + f"{skew}" + ") * cardinality(p.ips))::int] AS ip,\n"
        "         k.ids[x.i] AS kid,\n"
        "         k.uids[x.i] AS uid,\n"
        "         now() - random() * interval '" + f"{days} days" + "' AS ts,\n"
        "         random() AS s\n"
        "  FROM pool p, keys k,\n"
        "       generate_series(1, " + str(rows) + ") AS g,\n"
        "       LATERAL (SELECT 1 + floor(random() * cardinality(k.ids))::int AS i) AS x\n"
        ") AS t;\n"
    )
    out.write("COMMIT;\n")


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--rows", type=int, required=True, help="total number of reports to generate")
    ap.add_argument("--ips", type=int, default=100000, help="size of the IP pool (distinct addresses)")
    ap.add_argument("--days", type=float, default=7.0, help="spread reported_at over the last N days (max 7)")
    ap.add_argument("--chunk", type=int, default=500000, help="rows per INSERT transaction")
    ap.add_argument("--skew", type=float, default=2.0,
                    help="IP popularity skew: 1 = uniform, larger = a few IPs get most reports")
    ap.add_argument("--sources", type=parse_sources, default=parse_sources("auth:0.6,fail2ban:0.3,none:0.1"),
                    help="source weights, e.g. auth:0.6,fail2ban:0.3,none:0.1 ('none' = NULL)")
    ap.add_argument("--seed", type=int, default=1, help="seed for the IP pool and for setseed()")
    ap.add_argument("-o", "--output", default="-", help="output file (default: stdout)")
    args = ap.parse_args(argv)

    if args.rows < 1 or args.ips < 1 or args.chunk < 1:
        ap.error("--rows, --ips and --chunk must be positive")
    if not 0 < args.days <= 7:
        ap.error("--days must be in (0, 7]; older rows are pruned")
    if args.skew < 1:
        ap.error("--skew must be >= 1")

    rng = random.Random(args.seed)
    ips = public_ips(args.ips, rng)
    pool_sql = ",".join("'" + ip + "'" for ip in ips)

    out = sys.stdout if args.output == "-" else open(args.output, "w", encoding="utf-8")
    try:
        out.write(
            f"-- generated by gen_report_sql.py: rows={args.rows} ips={args.ips} "
            f"days={args.days} seed={args.seed} skew={args.skew}\n"
            f"SELECT setseed({(args.seed % 1000) / 1000:.3f});\n"
        )
        remaining = args.rows
        while remaining > 0:
            n = min(args.chunk, remaining)
            write_chunk(out, pool_sql, n, args.days, args.sources, args.skew)
            remaining -= n
    finally:
        if out is not sys.stdout:
            out.close()


if __name__ == "__main__":
    main()
