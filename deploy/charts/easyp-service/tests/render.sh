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
# Usage: deploy/charts/easyp-service/tests/render.sh

set -euo pipefail

CHART="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Resolved from the chart rather than from the caller's directory, and named so
# that moving the chart again is one edit here instead of a count of `..`.
REPO="$(cd "$CHART/../../.." && pwd)"

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

# config_yml prints the service configuration out of a rendered ConfigMap.
#
# The chart used to configure the service through forty environment variables
# written out by hand in deployment.yaml; it now renders the same config file
# every other deployment uses, so the checks below look inside that file.
config_yml() {
  awk '/^  config\.yml: \|$/ {found = 1; next}
       found && /^(---|[^ ])/ {exit}
       found {sub(/^    /, ""); print}' <<<"$1"
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
if expect_render "two keys reach the config file" \
  --set "config.license.publicKeys.2026-08=$KEY_A" \
  --set "config.license.publicKeys.2026-09=$KEY_B"; then

  cfg="$(config_yml "$out")"

  if grep -qF "\"2026-08\": \"$KEY_A\"" <<<"$cfg" && grep -qF "\"2026-09\": \"$KEY_B\"" <<<"$cfg"; then
    pass "two keys reach the config file"
  else
    fail "two keys reach the config file: both key ids expected under license.public_keys"$'\n'"$cfg"
  fi
fi

# Clearing the map must actually clear it — that is how an installation opts out
# of trusting easyp.tech and verifies against its own key only.
if expect_render "clearing the keys removes the section" \
  --set "config.license.publicKeys=null"; then

  if grep -q "public_keys" <<<"$(config_yml "$out")"; then
    fail "clearing the keys removes the section: license.public_keys rendered anyway"
  else
    pass "clearing the keys removes the section"
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
  if grep -qF "\"$EXPECTED_DEFAULT_KID\": \"$EXPECTED_DEFAULT_KEY\"" <<<"$(config_yml "$out")"; then
    pass "the defaults ship the published public key"
  else
    fail "the defaults ship the published public key: expected '$EXPECTED_DEFAULT_KID' under license.public_keys"
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

# The MCP endpoint is opt-in. Off (the default), nothing may publish the port:
# not the container, not the Service, not the config file — a port that is
# closed in one place and open in another is worse than either.
if expect_render "mcp off by default: no listener, no service port, no config section"; then
  if grep -qE '^\s+- name: mcp$' <<<"$out"; then
    fail "mcp off by default: the rendered output still names an mcp port"
  elif grep -qE '^mcp:$' <<<"$(config_yml "$out")"; then
    fail "mcp off by default: config.yml still carries an mcp section"
  else
    pass "mcp off by default: no listener, no service port, no config section"
  fi
fi

if expect_render "mcp.enabled publishes the port and the config" \
  --set mcp.enabled=true; then
  if ! grep -qE '^\s+- name: mcp$' <<<"$out"; then
    fail "mcp.enabled publishes the port and the config: no mcp port in the rendered output"
  elif ! grep -qE '^  enabled: true$' <<<"$(config_yml "$out")"; then
    fail "mcp.enabled publishes the port and the config: config.yml does not enable mcp"
  else
    pass "mcp.enabled publishes the port and the config"
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
echo "== the cache ceiling holds on both storage paths =="

# This guard once sat behind persistence.enabled, so turning persistence off
# removed the ceiling while cacheMaxBytes stayed where it was — and the
# ephemeral path is the one where an overrun fills the node's disk and the
# kubelet may evict a neighbouring pod instead of this one.
expect_failure "an emptyDir smaller than the cache limit is rejected" "must exceed config.registry.cacheMaxBytes" \
  --set persistence.enabled=false \
  --set persistence.ephemeralSizeLimit=10Gi \
  --set resources.limits.ephemeral-storage=30Gi

# An emptyDir counts against the pod's ephemeral-storage limit, so the two
# ceilings sit over the same bytes and the lower one decides. Contradictory,
# the pod is evicted well short of the number the operator set.
expect_failure "an ephemeral-storage limit below the emptyDir limit is rejected" "would evict the pod at the resource limit" \
  --set persistence.enabled=false

if expect_render "the emptyDir carries a size limit" \
     --set persistence.enabled=false \
     --set resources.limits.ephemeral-storage=30Gi; then
  if grep -qE 'sizeLimit: 25Gi' <<<"$out"; then
    pass "the emptyDir carries a size limit"
  else
    fail "the emptyDir carries a size limit: emptyDir rendered without sizeLimit"$'\n'"$(grep -A2 emptyDir <<<"$out")"
  fi
fi

# The scheduler places pods on what it was told to reserve; storage it does not
# know about is storage it will happily overcommit.
if expect_render "ephemeral storage is declared" ; then
  if grep -qE 'ephemeral-storage:' <<<"$out"; then
    pass "ephemeral storage is declared"
  else
    fail "ephemeral storage is declared: no ephemeral-storage in resources"
  fi
fi

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
  cfg="$(config_yml "$out")"
  # -e, because the pattern starts with a dash and would otherwise be read as an
  # option by some greps.
  if grep -qF -e '- "10.42.0.0/16"' <<<"$cfg" && grep -qF -e '- "10.43.0.0/16"' <<<"$cfg"; then
    pass "trusted proxies reach the container"
  else
    fail "trusted proxies reach the container: server.trusted_proxies not rendered as expected"$'\n'"$cfg"
  fi
fi

# The default is empty, and that has to stay a real absence rather than an empty
# list: an empty CIDR list means "the peer is the client", which is correct for
# a listener reached directly.
if expect_render "no trusted proxies renders no key"; then
  if grep -q "trusted_proxies" <<<"$(config_yml "$out")"; then
    fail "no trusted proxies renders no key: the key appeared anyway"
  else
    pass "no trusted proxies renders no key"
  fi
fi

echo
echo "== message size limits =="

# 67108864 through `quote` alone renders as 6.7108864e+07 and the service
# refuses to start. That defect has shipped once already, on a different value.
if expect_render "message size limits render as integers"; then
  cfg="$(config_yml "$out")"
  bad="$(grep -E '^\s*max_(recv|send)_msg_size: [0-9.]+e\+[0-9]+' <<<"$cfg" || true)"
  if [[ -n "$bad" ]]; then
    fail "message size limits render as integers: scientific notation"$'\n'"$bad"
  elif grep -qE '^\s*max_send_msg_size: 67108864$' <<<"$cfg"; then
    pass "message size limits render as integers"
  else
    fail "message size limits render as integers: 67108864 not found"$'\n'"$cfg"
  fi
fi

# The service rejects a send limit below the plugin output cap, so a chart that
# renders that combination would produce a pod that never starts.
if expect_render "send limit covers the plugin output cap"; then
  cfg="$(config_yml "$out")"
  send="$(grep -E '^\s*max_send_msg_size:' <<<"$cfg" | grep -oE '[0-9]+' | tail -1)"
  outmax="$(grep -E '^\s*max_output_size:' <<<"$cfg" | grep -oE '[0-9]+' | tail -1)"
  if [[ -n "$send" && -n "$outmax" && "$send" -ge "$outmax" ]]; then
    pass "send limit covers the plugin output cap"
  else
    fail "send limit covers the plugin output cap: send=$send output=$outmax"
  fi
fi

echo
echo "== the rendered configuration =="

# The chart renders a config file now, so the service itself can be asked
# whether it is one it would start on. This is the check the forty
# hand-maintained environment variables could not have: the names had to be
# right, and nothing but a deploy would say whether they were.
#
# Skipped rather than failed without a Go toolchain, so the chart stays testable
# on a machine that only has helm.
if ! command -v go >/dev/null 2>&1; then
  printf '  skip  the service accepts the rendered config (no go toolchain)\n'
elif expect_render "the service accepts the rendered config"; then
  rendered="$(mktemp)"
  config_yml "$out" >"$rendered"

  # The DSN is deliberately absent from the ConfigMap — it is a credential and
  # arrives from the secret — so it is supplied here the way the pod gets it.
  if verdict="$(cd "$REPO" && DB_POSTGRES_DSN='postgres://u:p@h:5432/easyp?sslmode=require' \
       go run ./cmd/easyp-svc config validate --cfg "$rendered" 2>&1)"; then
    pass "the service accepts the rendered config"
  else
    fail "the service accepts the rendered config"$'\n'"$verdict"
  fi

  # Which of the chart's numbers actually differ from the ones compiled into the
  # binary. Anything beyond the list below is drift between values.yaml and
  # internal/config — which is exactly how worker_pool.max_retries came to say 2
  # here and 3 in the compose configs, unnoticed, for months.
  #
  # Expected, and each for a reason:
  #   server.port.*        the chart uses 8080-8083, the binary defaults to 23410+
  #   db.postgres          from the secret, above
  #   license.public_keys  easyp.tech's published verification key
  # `|| true`, because the good outcome here is grep matching nothing, and under
  # `set -e` an empty match in a command substitution ends the script — silently,
  # as a clean exit partway through the suite.
  changed="$(cd "$REPO" && DB_POSTGRES_DSN='postgres://u:p@h:5432/easyp?sslmode=require' \
       go run ./cmd/easyp-svc config print --cfg "$rendered" --changed 2>/dev/null \
       | grep -E '^[a-z_]+:|^    [a-z_]+:|^  [a-z_]+:' \
       | grep -vE '^(server|db|license):$|^  port:$|^    (grpc|metric|health|mcp):|^  postgres:|^  public_keys:' || true)"

  if [[ -z "$changed" ]]; then
    pass "values.yaml has not drifted from the binary's defaults"
  else
    fail "values.yaml has not drifted from the binary's defaults: unexpected overrides"$'\n'"$changed"$'\n'"Either the chart or internal/config changed without the other; reconcile them, or extend the expected list above with the reason."
  fi

  rm -f "$rendered"
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
     --set persistence.enabled=false \
     --set resources.limits.ephemeral-storage=30Gi; then
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

  # Having the docker command is not the same as having a daemon to talk to,
  # and the difference showed: with Docker Desktop stopped this reported
  # "promtool rejected the rules" three times over, which sends the reader to
  # look for a PromQL error that does not exist. An unreachable daemon is the
  # same situation as no docker at all — the rules went unchecked — so it says
  # so.
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    docker run --rm -v "$file:/rules.yaml:ro" --entrypoint promtool \
      prom/prometheus:latest check rules /rules.yaml
    return
  fi

  return 2
}

# The X's are required: BSD mktemp accepts a template without them and GNU
# mktemp refuses, so this ran on a laptop and failed the Chart job on every CI
# run until someone read past the checks that had already passed.
rules_yaml="$(mktemp -t easyp-rules.XXXXXX)"
trap 'rm -f "$rules_yaml"' EXIT

if out="$(render --set prometheusRule.enabled=true --show-only templates/prometheusrule.yaml 2>&1)"; then
  # PrometheusRule wraps the rule groups under spec:. promtool wants them at the
  # top level, so drop everything above it and remove one level of indent.
  awk '/^spec:/{f=1;next} f{sub(/^  /,"");print}' <<<"$out" > "$rules_yaml"

  # mktemp creates the file 0600, and the promtool fallback bind-mounts it into
  # a container that runs as nobody. Docker Desktop masks ownership on the way
  # into its VM, so this only ever failed on Linux — with "permission denied" on
  # a file the runner had just written itself.
  chmod 0644 "$rules_yaml"

  if check="$(promtool_check "$rules_yaml" 2>&1)"; then
    pass "promtool accepts the rules ($(grep -c 'alert:' "$rules_yaml") alerts)"
  elif [[ $? -eq 2 ]]; then
    echo "  SKIP  promtool: no promtool, and no docker daemon to borrow one from"
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

# An alert that arrives with no procedure attached is where the time goes, so
# every one carries a runbook_url. The anchors are derived from alert names and
# the headings are written by hand, which is exactly the pair that drifts apart
# without something comparing them.
runbooks="$REPO/docs/RUNBOOKS.md"

# The URL the chart actually ships has to name that same file. Checking anchors
# in a path this script picks proves nothing about what a customer's alerts
# link to: 0.3.1 went out pointing at .spec/RUNBOOKS.md, deleted days later,
# and all 43 checks here still passed.
base_url="$(grep -oE 'runbookBaseUrl: .*' "$CHART/values.yaml" | awk '{print $2}')"
runbooks_rel="${runbooks#"$REPO/"}"

if [[ "$base_url" != *"/$runbooks_rel" ]]; then
  fail "runbookBaseUrl points at '$base_url', which does not end in '$runbooks_rel' — the alerts would link to a file that is not the one checked below"
else
  pass "runbookBaseUrl names the runbooks file this suite verifies"
fi

missing=""

while read -r alert; do
  if ! grep -q "runbook_url:.*#${alert,,}\"" "$rules_yaml"; then
    missing+=" $alert(no url)"
    continue
  fi

  if ! grep -qi "^## ${alert}\$" "$runbooks"; then
    missing+=" $alert(no heading)"
  fi
done < <(grep -oE '^\s*- alert: \w+' "$rules_yaml" | awk '{print $3}')

if [[ -z "$missing" ]]; then
  pass "every alert links to a runbook section that exists"
else
  fail "every alert links to a runbook section that exists:$missing"
fi

# The compose stack cannot use a PrometheusRule — there is no operator to
# collect one — so the same alerts exist a second time as a plain rule file for
# Mimir's ruler. Two copies with no mechanism in common is exactly the shape
# that drifts, so the names are compared here. The expressions are not: they
# differ in wording where the advice names a config key, and asserting on them
# would be asserting on prose.
compose_rules="$REPO/deploy/observability/mimir/rules/anonymous/easyp.yaml"

if [[ ! -f "$compose_rules" ]]; then
  fail "the compose stack has a copy of the rules: $compose_rules is missing"
else
  if check="$(promtool_check "$compose_rules" 2>&1)"; then
    pass "promtool accepts the compose rules ($(grep -c 'alert:' "$compose_rules") alerts)"
  elif [[ $? -eq 2 ]]; then
    echo "  SKIP  promtool: no promtool, and no docker daemon to borrow one from"
  else
    fail "promtool rejected the compose rules"$'\n'"$check"
  fi

  chart_alerts="$(grep -oE '^\s*- alert: \w+' "$rules_yaml" | awk '{print $3}' | sort)"
  compose_alerts="$(grep -oE '^\s*- alert: \w+' "$compose_rules" | awk '{print $3}' | sort)"

  if [[ "$chart_alerts" == "$compose_alerts" ]]; then
    pass "the chart and the compose stack alert on the same things"
  else
    fail "the chart and the compose stack alert on the same things"$'\n'"$(
      diff <(echo "$chart_alerts") <(echo "$compose_alerts") || true
    )"
  fi
fi

# The host rules are compose-only and have no counterpart in the chart, so they
# are checked for validity and for runbooks but not for parity. On Kubernetes the
# cluster owns node monitoring; an application chart with an opinion about node
# disk would duplicate it or disagree with it.
host_rules="$REPO/deploy/observability/mimir/rules/anonymous/host.yaml"

if [[ ! -f "$host_rules" ]]; then
  fail "the host rules exist: $host_rules is missing"
else
  if check="$(promtool_check "$host_rules" 2>&1)"; then
    pass "promtool accepts the host rules ($(grep -c 'alert:' "$host_rules") alerts)"
  elif [[ $? -eq 2 ]]; then
    echo "  SKIP  promtool: no promtool, and no docker daemon to borrow one from"
  else
    fail "promtool rejected the host rules"$'\n'"$check"
  fi

  host_missing=""
  while read -r alert; do
    if ! grep -q "runbook_url:.*#${alert,,}\"" "$host_rules"; then
      host_missing+=" $alert(no url)"
      continue
    fi

    if ! grep -qi "^## ${alert}\$" "$runbooks"; then
      host_missing+=" $alert(no heading)"
    fi
  done < <(grep -oE '^\s*- alert: \w+' "$host_rules" | awk '{print $3}')

  if [[ -z "$host_missing" ]]; then
    pass "every host alert links to a runbook section that exists"
  else
    fail "every host alert links to a runbook section that exists:$host_missing"
  fi
fi

echo "== compose mounts =="

# mimir.yaml names paths under /etc/mimir that only exist because a compose file
# mounts them, and the two live in different files. That split shipped a defect:
# the rules directory and the Alertmanager fallback were mounted in the
# observability overlay and not in docker-compose.yml, so the single-tier stack
# ran with a config pointing at nothing. Mimir does not complain — it starts, and
# has no alerting rules, which looks exactly like a stack with nothing wrong.
compose_files=(
  "$REPO/deploy/docker-compose.yml"
  "$REPO/deploy/docker-compose.observability.yml"
)

# Referenced paths, taken from the config rather than from a list kept by hand:
# a list kept by hand is the same failure one level up.
mimir_cfg="$REPO/deploy/observability/mimir/mimir.yaml"
referenced="$(grep -oE '/etc/mimir/[A-Za-z0-9._/-]+' "$mimir_cfg" | sort -u)"

unmounted=""
for compose in "${compose_files[@]}"; do
  grep -q '/etc/mimir/mimir.yaml' "$compose" || continue

  while read -r path; do
    [[ -z "$path" ]] && continue
    grep -q ":${path}:ro" "$compose" || unmounted+=" $(basename "$compose"):${path}"
  done <<<"$referenced"
done

if [[ -z "$unmounted" ]]; then
  pass "every path mimir.yaml names is mounted wherever mimir.yaml is ($(wc -l <<<"$referenced" | tr -d ' ') paths)"
else
  fail "every path mimir.yaml names is mounted wherever mimir.yaml is:$unmounted"
fi

echo "== image tag =="

# The default image tag is Chart.appVersion, and it has to be a tag the release
# workflow can actually produce. It was "0.9.0" while the registry only ever
# holds "v0.9.0", so a default install pulled a tag that has never existed. Two
# things hid it: the chart is installed nowhere, and `helm template` renders a
# wrong tag as readily as a right one.
release_pattern="$(grep -oE "v\[0-9\]\+\\\\?\.\[0-9\]\+\\\\?\.\[0-9\]\+" "$REPO/.github/workflows/release.yml" | head -1)"
app_version="$(grep -E '^appVersion:' "$CHART/Chart.yaml" | sed 's/^appVersion: *//; s/"//g')"

if [[ -z "$release_pattern" ]]; then
  fail "the release tag pattern is still in release.yml (it moved, so this check is blind)"
elif [[ "$app_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  pass "the default image tag ($app_version) is shaped like a published release"
else
  fail "the default image tag is shaped like a published release: Chart.appVersion is '$app_version', releases are tagged v0.0.0"
fi

echo "== certificates =="

# deploy/certs used to be mounted whole, which handed the CA's private key to
# every container including the two that execute plugin binaries. Whoever holds
# that key can issue a client certificate the service accepts, because the mTLS
# leg checks the signature and nothing else. The mounts are named file by file
# now; these two checks are what keeps them that way.
all_compose=("$REPO"/deploy/docker-compose*.yml)
certs_script="$REPO/deploy/scripts/gen-dev-certs.sh"

# A mount of the directory itself would reintroduce everything at once, so it is
# rejected as firmly as the key.
leaked=""
for compose in "${all_compose[@]}"; do
  while read -r mount; do
    [[ -z "$mount" ]] && continue
    case "$mount" in
      *ca.key*|*ca.srl*) leaked+=" $(basename "$compose"):${mount}" ;;
      ./certs:*)         leaked+=" $(basename "$compose"):${mount} (whole directory)" ;;
    esac
  done < <(grep -oE '\./certs[A-Za-z0-9._/-]*:[^"]*' "$compose")
done

if [[ -z "$leaked" ]]; then
  pass "no container is given the CA private key"
else
  fail "no container is given the CA private key:$leaked"
fi

# A named mount whose source does not exist is worse than a missing one: Docker
# creates a directory in its place and the service fails its TLS handshake with
# nothing that points at the cause. The generator is the authority on which
# files exist, so the names are checked against it rather than against a list.
missing=""
for compose in "${all_compose[@]}"; do
  while read -r file; do
    [[ -z "$file" ]] && continue
    base="${file%.*}"
    # issue <name> ... produces <name>.crt and <name>.key; ca.crt comes from the
    # openssl req that creates the authority itself.
    grep -qE "^issue ${base}( |$)|-keyout ${file}|-out ${file}" "$certs_script" \
      || missing+=" $(basename "$compose"):${file}"
  done < <(grep -oE '\./certs/[A-Za-z0-9._-]+' "$compose" | sed 's|\./certs/||' | sort -u)
done

if [[ -z "$missing" ]]; then
  pass "every certificate a compose file mounts is one gen-dev-certs.sh writes"
else
  fail "every certificate a compose file mounts is one gen-dev-certs.sh writes:$missing"
fi

echo
if [[ $failures -gt 0 ]]; then
  echo "$failures check(s) failed"
  exit 1
fi

echo "all checks passed"
