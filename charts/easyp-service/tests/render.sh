#!/usr/bin/env bash
#
# Render-time checks for the chart.
#
# These exist because a template is code that nothing else compiles. Two of the
# defects they cover shipped: a number rendered in scientific notation, which
# only showed up as a CrashLoopBackOff in a live cluster, and a values.yaml key
# with no corresponding env var, which showed up as a setting that silently did
# nothing. Both are visible in `helm template` output, if anyone looks.
#
# Usage: charts/easyp-service/tests/render.sh

set -euo pipefail

CHART="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Enough to get past the install-time guards, so each case below can change one
# thing without also having to satisfy the database and TLS preflight.
BASE=(
  --set secrets.create=true
  --set secrets.data.DB_POSTGRES_DSN=postgres://localhost/easyp
  --set tls.enabled=false
)

KEY_A="2e80e973708c58959e9cb575856094e9fa94bfeec29692b249df502750e1fb3a"
KEY_B="3f91fa84819d69a6afadc686967105fa0ba5caffd3a7a3ca35ae613861f20c4b"

failures=0

pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

render() {
  helm template test "$CHART" "${BASE[@]}" "$@"
}

# renders_with <description> -- <helm args...>: the template must render, and
# its output is left in $out for the caller to inspect.
expect_render() {
  local what="$1"; shift

  if ! out="$(render "$@" 2>&1)"; then
    fail "$what: helm template failed"$'\n'"$out"
    return 1
  fi

  return 0
}

expect_failure() {
  local what="$1" wanted="$2"; shift 2

  if out="$(render "$@" 2>&1)"; then
    fail "$what: expected helm template to fail, but it rendered"
    return
  fi

  if ! grep -qF "$wanted" <<<"$out"; then
    fail "$what: failed, but the message does not mention '$wanted'"$'\n'"$out"
    return
  fi

  pass "$what"
}

echo "== chart lint =="

# Passed the base values on purpose: linting the raw defaults only proves the
# install-time guards fire, which they are supposed to do.
if out="$(helm lint "$CHART" "${BASE[@]}" 2>&1)"; then
  pass "helm lint"
else
  fail "helm lint"$'\n'"$out"
fi

echo
echo "== licence public keys =="

# The setting must reach the container. Declaring it in values.yaml and
# rendering nothing is how it shipped the first time.
if expect_render "two keys render as LICENSE_PUBLIC_KEYS" \
  --set "config.license.publicKeys.2026-08=$KEY_A" \
  --set "config.license.publicKeys.2026-09=$KEY_B"; then

  wanted="2026-08:$KEY_A,2026-09:$KEY_B"

  if grep -qF "LICENSE_PUBLIC_KEYS" <<<"$out" && grep -qF "$wanted" <<<"$out"; then
    pass "two keys render as LICENSE_PUBLIC_KEYS"
  else
    fail "two keys render as LICENSE_PUBLIC_KEYS: expected '$wanted' in the rendered env"
  fi
fi

# Clearing the map must actually clear it — that is how an installation opts out
# of trusting easyp.tech and verifies against its own key only.
if expect_render "clearing the keys removes the variable" \
  --set "config.license.publicKeys=null"; then

  if grep -q "LICENSE_PUBLIC_KEYS" <<<"$out"; then
    fail "clearing the keys removes the variable: LICENSE_PUBLIC_KEYS rendered anyway"
  else
    pass "clearing the keys removes the variable"
  fi
fi

# The defaults carry easyp.tech's own published key, so a customer holding a
# licence token needs nothing else. It must survive rendering: dropping it turns
# every Enterprise licence into a silent community downgrade.
#
# Kept in step with keys/ in the licence registry by hand. If this fails after a
# key rotation, that is the check doing its job — update both.
EXPECTED_DEFAULT_KID="2026-08"
EXPECTED_DEFAULT_KEY="81322461987167d5cfd529e9cb8b96f4797f12fce6be4399a0866e250c9b6bb5"

if expect_render "the defaults ship the published public key"; then
  if grep -qF "$EXPECTED_DEFAULT_KID:$EXPECTED_DEFAULT_KEY" <<<"$out"; then
    pass "the defaults ship the published public key"
  else
    fail "the defaults ship the published public key: expected '$EXPECTED_DEFAULT_KID:$EXPECTED_DEFAULT_KEY' in the rendered env"
  fi
fi

# A private half is 128 hex characters. One reaching values.yaml would be
# published to every customer, and git history does not forget.
if expect_render "no private key reaches the rendered output"; then
  if grep -qE '\b[0-9a-fA-F]{128}\b' <<<"$out"; then
    fail "no private key reaches the rendered output: found a 128-hex-character string"
  else
    pass "no private key reaches the rendered output"
  fi
fi

expect_failure "a key that is not 64 hex characters is rejected" "64 hex characters" \
  --set "config.license.publicKeys.2026-08=deadbeef"

expect_failure "a non-hex key is rejected" "64 hex characters" \
  --set "config.license.publicKeys.2026-08=zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"

# A separator inside a key id would decode into a different map than the one
# written down, because the encoding is "<kid>:<hex>,<kid>:<hex>".
expect_failure "a key id containing a colon is rejected" "must not contain" \
  --set-string "config.license.publicKeys.bad=$KEY_A" \
  --set-json "config.license.publicKeys={\"a:b\":\"$KEY_A\"}"

echo
echo "== numbers survive rendering =="

# Helm parses YAML numbers as float64, so a large integer passed through `quote`
# comes out as 6.7108864e+07 and the service refuses to start. Templates must
# force these back to integers.
if expect_render "large byte counts render as integers" \
  --set config.registry.cacheMaxBytes=21474836480 \
  --set config.registry.maxOutputSize=67108864; then

  if grep -qE 'e\+[0-9]' <<<"$out"; then
    fail "large byte counts render as integers: found scientific notation"$'\n'"$(grep -E 'e\+[0-9]' <<<"$out")"
  else
    pass "large byte counts render as integers"
  fi
fi

expect_failure "a volume smaller than the cache limit is rejected" "must exceed config.registry.cacheMaxBytes" \
  --set persistence.size=10Gi

expect_failure "an unreadable volume size is rejected" "cannot read" \
  --set persistence.size=25TB

echo
echo "== trusted proxies =="

# Behind an ingress every request arrives from the controller's address. Without
# this set, one rate-limit bucket serves every caller and the audit log names the
# ingress rather than the client — neither of which fails visibly, which is why
# the chart refuses the combination instead of warning about it.
expect_failure "an ingress without trusted proxies is rejected" "trustedProxies" \
  --set ingress.enabled=true --set ingress.host=easyp.example.com \
  --set tls.enabled=false

if expect_render "trusted proxies reach the container" \
     --set 'config.server.trustedProxies[0]=10.42.0.0/16' \
     --set 'config.server.trustedProxies[1]=10.43.0.0/16'; then
  if grep -q 'value: "10.42.0.0/16,10.43.0.0/16"' <<<"$out"; then
    pass "trusted proxies reach the container"
  else
    fail "trusted proxies reach the container: SERVER_TRUSTED_PROXIES not rendered as expected"$'\n'"$(grep -A1 TRUSTED_PROXIES <<<"$out")"
  fi
fi

# The default is empty, and that has to stay a real absence rather than an empty
# string: an empty CIDR list means "the peer is the client", which is correct for
# a listener reached directly.
if expect_render "no trusted proxies renders no variable"; then
  if grep -q "SERVER_TRUSTED_PROXIES" <<<"$out"; then
    fail "no trusted proxies renders no variable: the variable appeared anyway"
  else
    pass "no trusted proxies renders no variable"
  fi
fi

echo
echo "== message size limits =="

# 67108864 through `quote` alone renders as 6.7108864e+07 and the service
# refuses to start. That defect has shipped once already, on a different value.
if expect_render "message size limits render as integers"; then
  bad="$(grep -E 'value: "[0-9.]+e\+[0-9]+"' <<<"$out" || true)"
  if [[ -n "$bad" ]]; then
    fail "message size limits render as integers: scientific notation"$'\n'"$bad"
  elif grep -q 'value: "67108864"' <<<"$out"; then
    pass "message size limits render as integers"
  else
    fail "message size limits render as integers: 67108864 not found"
  fi
fi

# The service rejects a send limit below the plugin output cap, so a chart that
# renders that combination would produce a pod that never starts.
if expect_render "send limit covers the plugin output cap"; then
  send="$(grep -A1 'SERVER_MAX_SEND_MSG_SIZE' <<<"$out" | grep -oE '[0-9]+' | tail -1)"
  outmax="$(grep -A1 'REGISTRY_MAX_OUTPUT_SIZE' <<<"$out" | grep -oE '[0-9]+' | tail -1)"
  if [[ -n "$send" && -n "$outmax" && "$send" -ge "$outmax" ]]; then
    pass "send limit covers the plugin output cap"
  else
    fail "send limit covers the plugin output cap: send=$send output=$outmax"
  fi
fi

echo
echo "== memory budget =="

# Plugin output is buffered whole, so peak memory tracks
# maxConcurrentGenerations × maxOutputSize (twice over, once marshalled). The
# defaults shipped as 16 × 64 MiB against a 1Gi limit — the buffers alone were
# the entire limit. An OOMKill presents as a crash, not as overload, so the
# arithmetic is only visible here.
if expect_render "the default resources cover peak buffers"; then
  pass "the default resources cover peak buffers"
fi

expect_failure "concurrency beyond the memory limit is rejected" "exceeds resources.limits.memory" \
  --set config.workerPool.maxConcurrentGenerations=64

expect_failure "a raised output cap beyond the memory limit is rejected" "exceeds resources.limits.memory" \
  --set config.registry.maxOutputSize=268435456

# Limits are optional. A namespace that sets them by policy must still be able
# to install, so the guard has nothing to check and stays out of the way.
if expect_render "no memory limit skips the check" \
     --set 'resources.limits=null' \
     --set config.workerPool.maxConcurrentGenerations=64; then
  pass "no memory limit skips the check"
fi

echo
echo "== rollout strategy =="

# A ReadWriteOnce volume attaches to one node at a time, so a rolling update can
# strand the replacement pod on a Multi-Attach error and hang the upgrade
# indefinitely. Nothing in `helm upgrade` reports that as a failure, which is
# why it is checked here rather than discovered in a cluster.
if expect_render "a ReadWriteOnce volume forces Recreate"; then
  if grep -q "type: Recreate" <<<"$out"; then
    pass "a ReadWriteOnce volume forces Recreate"
  else
    fail "a ReadWriteOnce volume forces Recreate: strategy is not Recreate"$'\n'"$(grep -A2 'strategy:' <<<"$out")"
  fi
fi

# The converse: a shared volume has no attach conflict, so the upgrade should
# roll rather than take the service down for it.
if expect_render "a ReadWriteMany volume rolls" \
     --set persistence.accessMode=ReadWriteMany; then
  if grep -q "type: RollingUpdate" <<<"$out"; then
    pass "a ReadWriteMany volume rolls"
  else
    fail "a ReadWriteMany volume rolls: strategy is not RollingUpdate"
  fi
fi

# Without persistence the volume is an emptyDir, which every pod gets its own
# copy of. Nothing to conflict over, so nothing to take downtime for.
if expect_render "no persistence rolls" \
     --set persistence.enabled=false; then
  if grep -q "type: RollingUpdate" <<<"$out"; then
    pass "no persistence rolls"
  else
    fail "no persistence rolls: strategy is not RollingUpdate"
  fi
fi

echo
echo "== network policy =="

# On by default because this pod executes third-party binaries. The check is that
# the defaults produce a policy at all, and that the ports the service genuinely
# needs are in it — a NetworkPolicy missing 5432 is a pod that cannot reach its
# database, which presents as a hang rather than an error.
if expect_render "the defaults ship a network policy"; then
  if grep -q "kind: NetworkPolicy" <<<"$out"; then
    missing=""
    for port in 5432 443 4317 53; do
      grep -qE "port: $port$" <<<"$out" || missing="$missing $port"
    done

    if [[ -n "$missing" ]]; then
      fail "the defaults ship a network policy: egress is missing port(s)$missing"
    else
      pass "the defaults ship a network policy"
    fi
  else
    fail "the defaults ship a network policy: none rendered"
  fi
fi

echo
echo "== alerting rules =="

# A PromQL typo renders as valid YAML and then never fires. promtool is the only
# thing that reads the expressions rather than the shape around them.
promtool_check() {
  local file="$1"

  if command -v promtool >/dev/null 2>&1; then
    promtool check rules "$file"
    return
  fi

  if command -v docker >/dev/null 2>&1; then
    docker run --rm -v "$file:/rules.yaml:ro" --entrypoint promtool \
      prom/prometheus:latest check rules /rules.yaml
    return
  fi

  return 2
}

rules_yaml="$(mktemp -t easyp-rules)"
trap 'rm -f "$rules_yaml"' EXIT

if out="$(render --set prometheusRule.enabled=true --show-only templates/prometheusrule.yaml 2>&1)"; then
  # PrometheusRule wraps the rule groups under spec:. promtool wants them at the
  # top level, so drop everything above it and remove one level of indent.
  awk '/^spec:/{f=1;next} f{sub(/^  /,"");print}' <<<"$out" > "$rules_yaml"

  if check="$(promtool_check "$rules_yaml" 2>&1)"; then
    pass "promtool accepts the rules ($(grep -c 'alert:' "$rules_yaml") alerts)"
  elif [[ $? -eq 2 ]]; then
    echo "  SKIP  promtool: neither promtool nor docker is available"
  else
    fail "promtool rejected the rules"$'\n'"$check"
  fi
else
  fail "the alerting rules do not render"$'\n'"$out"
fi

# Zero means no licence at all, which is the community default. Without this
# guard in the expression the expiry alert fires forever on every install that
# has no licence.
if grep -q 'easyp_license_expiry_timestamp_seconds > 0' "$rules_yaml"; then
  pass "the expiry alert ignores installs with no licence"
else
  fail "the expiry alert ignores installs with no licence: guard missing from the expression"
fi

echo
if [[ $failures -gt 0 ]]; then
  echo "$failures check(s) failed"
  exit 1
fi

echo "all checks passed"
