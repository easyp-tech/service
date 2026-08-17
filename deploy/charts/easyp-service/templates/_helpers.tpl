{{/*
Name helpers, standard Helm shapes.
*/}}
{{- define "easyp-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "easyp-service.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "easyp-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "easyp-service.labels" -}}
helm.sh/chart: {{ include "easyp-service.chart" . }}
{{ include "easyp-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "easyp-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "easyp-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "easyp-service.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "easyp-service.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the secret carrying credentials. Either the one we render or the one
the operator brought.
*/}}
{{- define "easyp-service.secretName" -}}
{{- if .Values.secrets.existingSecret }}
{{- .Values.secrets.existingSecret }}
{{- else }}
{{- printf "%s-env" (include "easyp-service.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Secret holding the server certificate. cert-manager writes into a secret we
name ourselves, so the two paths converge here.
*/}}
{{- define "easyp-service.serverTLSSecret" -}}
{{- if .Values.tls.serverSecret }}
{{- .Values.tls.serverSecret }}
{{- else }}
{{- printf "%s-server-tls" (include "easyp-service.fullname" .) }}
{{- end }}
{{- end }}

{{- define "easyp-service.clientTLSSecret" -}}
{{- if .Values.ingress.serversTransport.clientSecret }}
{{- .Values.ingress.serversTransport.clientSecret }}
{{- else }}
{{- printf "%s-client-tls" (include "easyp-service.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Secret providing the CA that client certificates are checked against. An empty
value means "the ca.crt that ships alongside the server certificate", which is
what a cert-manager CA issuer produces.
*/}}
{{- define "easyp-service.clientCASecret" -}}
{{- if .Values.tls.clientCASecret }}
{{- .Values.tls.clientCASecret }}
{{- else }}
{{- include "easyp-service.serverTLSSecret" . }}
{{- end }}
{{- end }}

{{- define "easyp-service.mutualTLS" -}}
{{- and .Values.tls.enabled (ne .Values.tls.clientCASecret "-") -}}
{{- end }}

{{/*
Converts a Kubernetes quantity like "25Gi" to plain bytes, so that the volume
size and the cache limit can be compared. Only the suffixes a volume size
realistically carries are handled; anything else fails loudly rather than
comparing nonsense.
*/}}
{{- define "easyp-service.toBytes" -}}
{{- $q := . | toString -}}
{{- if hasSuffix "Gi" $q -}}
{{- mulf (trimSuffix "Gi" $q | float64) 1073741824 | int64 -}}
{{- else if hasSuffix "Mi" $q -}}
{{- mulf (trimSuffix "Mi" $q | float64) 1048576 | int64 -}}
{{- else if hasSuffix "Ti" $q -}}
{{- mulf (trimSuffix "Ti" $q | float64) 1099511627776 | int64 -}}
{{- else if hasSuffix "G" $q -}}
{{- mulf (trimSuffix "G" $q | float64) 1000000000 | int64 -}}
{{- else if hasSuffix "M" $q -}}
{{- mulf (trimSuffix "M" $q | float64) 1000000 | int64 -}}
{{- else if regexMatch "^[0-9]+$" $q -}}
{{- $q -}}
{{- else -}}
{{- fail (printf "easyp-service: cannot read %q as a size; use Mi, Gi, Ti, M, G or plain bytes." $q) -}}
{{- end -}}
{{- end }}

{{/*
Encodes config.license.publicKeys as LICENSE_PUBLIC_KEYS: "<kid>:<hex>,<kid>:<hex>".

Both separators are rejected inside a key id, because a key id carrying one
would silently produce a different map than the one written in values.yaml.
Range over a map yields keys in sorted order, so the rendered value does not
churn between otherwise identical templates.
*/}}
{{- define "easyp-service.licensePublicKeys" -}}
{{- $pairs := list -}}
{{- range $kid, $hex := .Values.config.license.publicKeys -}}
  {{- if or (contains ":" $kid) (contains "," $kid) -}}
    {{- fail (printf "easyp-service: config.license.publicKeys key id %q must not contain ':' or ','." $kid) -}}
  {{- end -}}
  {{- if not (regexMatch "^[0-9a-fA-F]{64}$" (trim $hex)) -}}
    {{- fail (printf "easyp-service: config.license.publicKeys[%s] must be a hex-encoded Ed25519 public key (64 hex characters), got %q." $kid $hex) -}}
  {{- end -}}
  {{- $pairs = append $pairs (printf "%s:%s" $kid (trim $hex)) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end }}

{{/*
Preflight checks.

Every one of these fails a `helm install` that would otherwise produce a pod
that starts and then misbehaves in a way that is tedious to diagnose from
inside the cluster.
*/}}
{{- define "easyp-service.validate" -}}

{{/*
Licence key ids and hex are checked here rather than only where they render.
They used to be validated as a side effect of building the LICENSE_PUBLIC_KEYS
variable in deployment.yaml; the ConfigMap renders the map directly, so without
this a malformed key would reach the pod and downgrade it to community mode with
nothing but a log line to say so.
*/}}
{{- if .Values.config.license.publicKeys }}
{{- $_ := include "easyp-service.licensePublicKeys" . }}
{{- end }}

{{- if and (not .Values.secrets.existingSecret) (not .Values.secrets.create) }}
{{- fail "easyp-service: set secrets.existingSecret to a secret holding DB_POSTGRES_DSN, or secrets.create=true with secrets.data. The service cannot start without a database DSN." }}
{{- end }}

{{- if and .Values.secrets.create (not .Values.secrets.data.DB_POSTGRES_DSN) }}
{{- fail "easyp-service: secrets.create is true but secrets.data.DB_POSTGRES_DSN is empty." }}
{{- end }}

{{- if gt (int .Values.replicaCount) 1 }}
{{- if and .Values.persistence.enabled (eq .Values.persistence.accessMode "ReadWriteOnce") }}
{{- fail "easyp-service: replicaCount > 1 needs persistence.accessMode=ReadWriteMany, otherwise only one pod can mount the plugin cache and the rest stay Pending." }}
{{- end }}
{{- end }}

{{/*
Eviction triggers at cacheMaxBytes, so whatever the cache is written to has to
be bigger than that or the disk fills before the cache ever decides it is full.
A full volume fails generation with an I/O error rather than anything that names
the real cause, which is why this is worth refusing at install time.

Checked on both storage paths deliberately. This guard once sat behind
`persistence.enabled`, which meant turning persistence off silently took the
ceiling away while cacheMaxBytes stayed where it was — and the ephemeral path is
where an overrun hurts most, because it is the node's disk that fills and the
kubelet may evict a neighbour rather than the pod responsible.
*/}}
{{- if gt (int64 .Values.config.registry.cacheMaxBytes) 0 }}
{{- $cache := int64 .Values.config.registry.cacheMaxBytes }}
{{- $sizeKey := ternary "persistence.size" "persistence.ephemeralSizeLimit" .Values.persistence.enabled }}
{{- $size := ternary .Values.persistence.size .Values.persistence.ephemeralSizeLimit .Values.persistence.enabled }}
{{- $volume := include "easyp-service.toBytes" $size | int64 }}
{{- if le $volume $cache }}
{{- fail (printf "easyp-service: %s (%s, %d bytes) must exceed config.registry.cacheMaxBytes (%d bytes). Eviction starts at the cache limit, so storage sized to it is already full by the time the cache needs room." $sizeKey $size $volume $cache) }}
{{- end }}
{{- end }}

{{/*
An emptyDir counts against the pod's ephemeral-storage limit, so with
persistence off there are two ceilings over the same bytes and the lower one
wins. Left contradictory, the kubelet evicts the pod at the resource limit while
persistence.ephemeralSizeLimit still reads as the real bound — the pod dies well
short of the number the operator set, and nothing says why.

The shipped ephemeral-storage figures assume the default, persistence enabled,
where the cache lives on its own volume and only logs and the writable layer are
charged here. Turning persistence off means raising them.
*/}}
{{- if not .Values.persistence.enabled }}
{{- if .Values.resources.limits }}
{{- if index .Values.resources.limits "ephemeral-storage" }}
{{- $limit := include "easyp-service.toBytes" (index .Values.resources.limits "ephemeral-storage") | int64 }}
{{- $emptyDir := include "easyp-service.toBytes" .Values.persistence.ephemeralSizeLimit | int64 }}
{{- if le $limit $emptyDir }}
{{- fail (printf "easyp-service: with persistence disabled the plugin cache lives in an emptyDir, which counts against resources.limits.ephemeral-storage (%s, %d bytes) — currently at or below persistence.ephemeralSizeLimit (%s, %d bytes). The kubelet would evict the pod at the resource limit, before the cache ever reached its own. Raise resources.limits.ephemeral-storage (and requests, so the scheduler reserves the room) above the emptyDir limit, or lower persistence.ephemeralSizeLimit." (index .Values.resources.limits "ephemeral-storage") $limit .Values.persistence.ephemeralSizeLimit $emptyDir) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Peak memory against the pod's limit. The plugin's output is read into memory in
one piece, so the buffers alone come to maxConcurrentGenerations ×
maxOutputSize, and the same bytes exist a second time once marshalled into the
gRPC response — hence the factor of two. Anything else the pod needs comes out
of what is left.

Shipped once as 16 × 64 MiB against a 1Gi limit, i.e. the buffers accounted for
the entire limit on their own. The pod is OOMKilled, which presents as a crash
rather than as overload: nothing logged, no rejected-generation counter, no
saturation alert. Refusing the arithmetic at install time is the only place it
is visible.

Skipped when no memory limit is set: limits are optional, and a namespace that
sets them by policy should not be unable to install the chart.
*/}}
{{- if .Values.resources.limits }}
{{- if .Values.resources.limits.memory }}
{{- $limit := include "easyp-service.toBytes" .Values.resources.limits.memory | int64 }}
{{- $concurrent := int64 .Values.config.workerPool.maxConcurrentGenerations }}
{{- $output := int64 .Values.config.registry.maxOutputSize }}
{{- $peak := mul $concurrent $output 2 | int64 }}
{{- if gt $peak $limit }}
{{- fail (printf "easyp-service: config.workerPool.maxConcurrentGenerations (%d) × config.registry.maxOutputSize (%d bytes) × 2 = %d bytes exceeds resources.limits.memory (%s, %d bytes). Plugin output is buffered whole and exists again once marshalled, so this is the floor the pod needs before anything else; exceeding it means an OOMKill under load, which looks like a crash rather than overload. Lower maxConcurrentGenerations or maxOutputSize, or raise the memory limit." $concurrent $output $peak .Values.resources.limits.memory $limit) }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Shutdown budget, innermost first. Checking only the outer pair would let the
process kill itself mid-generation while the grace period still looked generous.
*/}}
{{- $grace := int .Values.terminationGracePeriodSeconds }}
{{- $force := int .Values.config.forceShutdownAfterSeconds }}
{{- $gen := int .Values.config.workerPool.generationTimeoutSeconds }}
{{- if le $force $gen }}
{{- fail (printf "easyp-service: config.forceShutdownAfterSeconds (%d) must exceed config.workerPool.generationTimeoutSeconds (%d), otherwise the process exits while a generation it accepted is still running. The service refuses to start with this combination." $force $gen) }}
{{- end }}
{{- if le $grace $force }}
{{- fail (printf "easyp-service: terminationGracePeriodSeconds (%d) must exceed config.forceShutdownAfterSeconds (%d), otherwise Kubernetes sends SIGKILL before the process has used its own shutdown budget." $grace $force) }}
{{- end }}

{{- if .Values.tls.enabled }}
{{- if and (not .Values.tls.serverSecret) (not .Values.certManager.enabled) }}
{{- fail "easyp-service: tls.enabled requires either tls.serverSecret or certManager.enabled." }}
{{- end }}
{{- end }}

{{/*
The ServersTransport tells the router which client certificate to present on
the mTLS leg, and the chart creates no such Secret — it can only be supplied.
Left empty it fell back to a generated name, "<fullname>-client-tls", that
exists nowhere: the resource rendered, applied, and pointed at nothing, and the
first sign of it would have been the router failing every request to a listener
that was working fine. The server certificate has been guarded this way from
the start; this is the other half of the same pair.
*/}}
{{- if and .Values.ingress.enabled .Values.tls.enabled .Values.ingress.serversTransport.enabled }}
{{- if not .Values.ingress.serversTransport.clientSecret }}
{{- fail "easyp-service: ingress.serversTransport.enabled requires ingress.serversTransport.clientSecret — a kubernetes.io/tls Secret with the client certificate the router presents. The chart does not create one." }}
{{- end }}
{{- end }}

{{- if .Values.certManager.enabled }}
{{- if not .Values.certManager.issuerRef.name }}
{{- fail "easyp-service: certManager.enabled requires certManager.issuerRef.name." }}
{{- end }}
{{- end }}

{{- if .Values.ingress.enabled }}
{{- if not .Values.ingress.host }}
{{- fail "easyp-service: ingress.enabled requires ingress.host." }}
{{- end }}
{{/*
Refused rather than warned about, because the failure it prevents is silent.
Behind an ingress every request reaches the pod from the controller's address,
so without trusted proxies configured the rate limit and the per-caller
concurrency limit collapse into a single bucket shared by every client, and the
audit log attributes every action to the ingress. The install succeeds, the pod
runs, and nothing says otherwise.
*/}}
{{- if not .Values.config.server.trustedProxies }}
{{- fail "easyp-service: ingress.enabled requires config.server.trustedProxies. Behind a proxy every caller arrives from the proxy's address, so with this empty the rate limit and per-client concurrency limit apply to all callers combined, and the audit log records the ingress rather than who acted. Set it to the CIDR your ingress controller's pods run in." }}
{{- end }}
{{- if and .Values.tls.enabled .Values.ingress.serversTransport.enabled }}
{{- if and (not .Values.ingress.serversTransport.clientSecret) (not .Values.certManager.clientCertificate.enabled) }}
{{- fail "easyp-service: the gRPC listener demands a client certificate, so the router needs one. Set ingress.serversTransport.clientSecret or enable certManager.clientCertificate." }}
{{- end }}
{{- end }}
{{- if and .Values.tls.enabled (not .Values.ingress.serversTransport.enabled) }}
{{- fail "easyp-service: tls.enabled with ingress.serversTransport.enabled=false would send plaintext to a TLS listener. Disable tls or enable the ServersTransport." }}
{{- end }}
{{- end }}

{{- end }}
