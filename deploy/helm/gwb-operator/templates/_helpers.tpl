{{/*
Name helpers — trimmed to 63 chars so anything that becomes a k8s
object name is DNS-1123 compliant. fullname stays prefixed with the
release name so two installs into the same namespace don't collide.
*/}}

{{- define "gwb-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gwb-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "gwb-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Standard labels — applied to every object the chart creates. The
control-plane label is retained so existing selectors (ServiceMonitor,
metrics Service) keep working if someone migrates from a raw
kustomize install.
*/}}
{{- define "gwb-operator.labels" -}}
helm.sh/chart: {{ include "gwb-operator.chart" . }}
{{ include "gwb-operator.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
control-plane: controller-manager
{{- end -}}

{{- define "gwb-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gwb-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
serviceAccountName resolves the name used on the Deployment.
*/}}
{{- define "gwb-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "gwb-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
webhookServiceName / webhookCertName — derived so templates stay in
agreement on the cert → Service → Webhook chain without threading a
shared variable through every file.
*/}}
{{- define "gwb-operator.webhookServiceName" -}}
{{- printf "%s-webhook" (include "gwb-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gwb-operator.webhookCertName" -}}
{{- printf "%s-serving-cert" (include "gwb-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gwb-operator.webhookCertSecretName" -}}
{{- printf "%s-webhook-tls" (include "gwb-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gwb-operator.metricsServiceName" -}}
{{- printf "%s-metrics" (include "gwb-operator.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
