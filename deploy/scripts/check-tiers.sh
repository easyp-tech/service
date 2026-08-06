#!/usr/bin/env bash
#
# Asserts that the two-tier dev stack actually has two tiers.
#
# This exists because the failure it catches is silent. A licence that is
# missing, empty or unverifiable does not stop the service: community is a
# legitimate configuration, so it logs the problem and serves on. The result is
# two identical community containers answering health checks, routing through
# traefik and proving nothing — which is exactly the state the dev host sat in
# for eight hours before anyone looked at a metric.
#
# easyp_license_valid is the discriminator: 1 for enterprise, 0 for community.
# Both sides are asserted. A community container reading 1 is just as broken as
# an enterprise one reading 0 — it means the licence reached the control group,
# and the pair can no longer tell you anything by comparison.
#
# Lives in deploy/scripts/ rather than in Taskfile.yml because the deployment
# directory is copied to hosts that carry no source and no task runner. One
# implementation, run in both places, cannot drift from itself.
#
# Usage:
#   ./check-tiers.sh
#   COMMUNITY_METRICS_PORT=18081 ENTERPRISE_METRICS_PORT=19081 ./check-tiers.sh
#   HOST=example.internal ./check-tiers.sh

set -euo pipefail

HOST="${HOST:-localhost}"
COMMUNITY_METRICS_PORT="${COMMUNITY_METRICS_PORT:-8081}"
ENTERPRISE_METRICS_PORT="${ENTERPRISE_METRICS_PORT:-9081}"

# "absent" rather than an empty string: a metric that was never registered and a
# metric reading 0 are different diagnoses, and collapsing them sends whoever
# reads this output looking in the wrong place.
read_valid() {
    local port="$1"

    curl -sf --max-time 10 "http://${HOST}:${port}/metrics" 2>/dev/null \
        | awk '/^easyp_license_valid /{print $2; found=1} END{if (!found) print "absent"}'
}

community="$(read_valid "$COMMUNITY_METRICS_PORT")"
enterprise="$(read_valid "$ENTERPRISE_METRICS_PORT")"

printf '  community   :%s  easyp_license_valid = %s  (want 0)\n' \
    "$COMMUNITY_METRICS_PORT" "$community"
printf '  enterprise  :%s  easyp_license_valid = %s  (want 1)\n' \
    "$ENTERPRISE_METRICS_PORT" "$enterprise"

failed=0

if [ "$enterprise" = "absent" ]; then
    echo
    echo "The enterprise container did not answer on :${ENTERPRISE_METRICS_PORT}."
    echo "It is down, still starting, or bound somewhere else — see EASYP_BIND."
    failed=1
elif [ "$enterprise" != "1" ]; then
    echo
    echo "The enterprise container is not running as enterprise. Its licence either"
    echo "never arrived or failed to verify; either way the service logged it and"
    echo "carried on in community mode. Check LICENSE_KEY and LICENSE_PUBLIC_KEY in"
    echo "the .env the stack was started with, then read the logs:"
    echo "  docker logs easyp-api-enterprise 2>&1 | grep -i licen | tail"
    failed=1
fi

if [ "$community" = "absent" ]; then
    echo
    echo "The community container did not answer on :${COMMUNITY_METRICS_PORT}."
    failed=1
elif [ "$community" != "0" ]; then
    echo
    echo "The community container is also enterprise, so the stack cannot tell the"
    echo "tiers apart. A licence is reaching the control group — check that LICENSE_*"
    echo "is set on service-enterprise only."
    failed=1
fi

if [ "$failed" -ne 0 ]; then
    exit 1
fi

echo
echo "OK: enterprise is licensed, community is not."

# The licence manager re-reads on an interval (license.cache_ttl, 5m by default),
# so a reading taken seconds after startup reflects the first refresh and not
# necessarily the steady state.
echo "Re-run once license.cache_ttl has elapsed to confirm it holds."
