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
Preflight checks.

Every one of these fails a `helm install` that would otherwise produce a pod
that starts and then misbehaves in a way that is tedious to diagnose from
inside the cluster.
*/}}
{{- define "easyp-service.validate" -}}

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

{{- if .Values.certManager.enabled }}
{{- if not .Values.certManager.issuerRef.name }}
{{- fail "easyp-service: certManager.enabled requires certManager.issuerRef.name." }}
{{- end }}
{{- end }}

{{- if .Values.ingress.enabled }}
{{- if not .Values.ingress.host }}
{{- fail "easyp-service: ingress.enabled requires ingress.host." }}
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
