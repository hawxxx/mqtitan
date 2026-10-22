#!/usr/bin/env bash
# End-to-end proof of broker balance telemetry against three EMQX brokers.
# Needs docker with compose v2 and python3. Builds the mqtitan image from the
# repository root unless MQTITAN_IMAGE names an existing image.
set -uo pipefail

cd "$(dirname "$0")" || exit 1
compose=(docker compose -f compose.yaml)
fail=0

cleanup() { "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1; }
trap cleanup EXIT

pass() { echo "PASS $1"; }
bad() {
  echo "FAIL $1: $2" >&2
  fail=1
}

if [ -z "${MQTITAN_IMAGE:-}" ]; then
  "${compose[@]}" build --quiet controller || exit 1
fi
"${compose[@]}" up -d --wait broker-a broker-b broker-c prometheus controller || exit 1

# Wait until Prometheus has scraped every target in both jobs.
for _ in $(seq 1 60); do
  n=$("${compose[@]}" exec -T prometheus wget -qO- 'http://localhost:9090/api/v1/targets?state=active' 2>/dev/null |
    python3 -c 'import sys,json; print(sum(1 for t in json.load(sys.stdin)["data"]["activeTargets"] if t["health"]=="up"))' 2>/dev/null || echo 0)
  [ "$n" = 6 ] && break
  sleep 2
done
[ "$n" = 6 ] || {
  echo "prometheus targets not up ($n/6)" >&2
  exit 1
}

# run_scenario NAME: sets $code, $out and $result (results JSON).
run_scenario() {
  out=$("${compose[@]}" run --rm -T mqtitan run "/e2e/$1.yaml" 2>&1)
  code=$?
  echo "$out" | tail -4
  id=$(echo "$out" | sed -n 's/^Test //p' | head -1)
  result=$("${compose[@]}" run --rm -T mqtitan results "$id" 2>/dev/null)
}

# observed NAME: prints the observed value of a failed threshold, if any.
observed() {
  RESULT="$result" python3 - "$1" <<'PY'
import json, os, sys
d = json.loads(os.environ["RESULT"])
d = d.get("data", d)
for t in d["thresholds"]:
    if t["name"] == sys.argv[1] and not t["passed"]:
        print(t["observed"])
PY
}

# balanced: exit 0, three addresses with more than 20% of connects each,
# churn counters, three DNS answers, and no connect failures.
run_scenario balanced
if [ "$code" != 0 ]; then
  bad balanced "exit $code"
elif RESULT="$result" python3 - <<'PY'
import json, os, sys
d = json.loads(os.environ["RESULT"])
s = d.get("data", d)["snapshot"]
addrs = s["remoteAddresses"]
total = sum(a["connects"] for a in addrs)
print("  shares:", {a["key"]: round(100 * a["connects"] / total, 1) for a in addrs},
      "churned:", s["churned"], "churnReconnects:", s["churnReconnects"],
      "dnsAnswers:", [a["key"] for a in s["dnsAnswers"]],
      "connectFailures:", s.get("connectFailures") or {})
ok = (len(addrs) == 3 and all(a["connects"] * 5 > total for a in addrs)
      and s["churned"] > 0 and s["churnReconnects"] > 0
      and len(s["dnsAnswers"]) == 3
      and not s.get("connectFailures"))
sys.exit(0 if ok else 1)
PY
then
  pass balanced
else
  bad balanced "result assertions failed"
fi

# skewed: one cached broker address makes broker_connection_cv fail.
run_scenario skewed
if [ "$code" = 0 ]; then
  bad skewed-fails "expected non-zero exit"
elif ! awk -v v="$(observed broker_connection_cv)" 'BEGIN { exit !(v ~ /^[0-9.]+%$/ && v + 0 > 10) }'; then
  bad skewed-fails "broker_connection_cv observed '$(observed broker_connection_cv)', want a percentage above 10"
else
  echo "  broker_connection_cv observed: $(observed broker_connection_cv)"
  pass skewed-fails
fi

# nozone: brokers without a zone label cannot satisfy zone_connection_skew.
run_scenario nozone
if [ "$code" = 0 ]; then
  bad nozone-fails "expected non-zero exit"
elif [ "$(observed zone_connection_skew)" != "no observations" ]; then
  bad nozone-fails "zone_connection_skew observed '$(observed zone_connection_skew)'"
else
  pass nozone-fails
fi

# missing-target: expectedTargets 4 but only three brokers exist.
run_scenario missing-target
if [ "$code" = 0 ]; then
  bad missing-target-fails "expected non-zero exit"
elif [ "$(observed broker_connection_cv)" != "incomplete broker telemetry" ]; then
  bad missing-target-fails "broker_connection_cv observed '$(observed broker_connection_cv)'"
else
  pass missing-target-fails
fi

exit "$fail"
