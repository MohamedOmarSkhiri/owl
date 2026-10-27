{{- define "owl.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "owl.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "owl.relay.name" -}}{{ include "owl.fullname" . }}-relay{{- end -}}
{{- define "owl.tokens.name" -}}{{ default (printf "%s-tokens" (include "owl.fullname" .)) .Values.auth.existingSecret }}{{- end -}}
{{- define "owl.tls.name" -}}{{ default (printf "%s-tls" (include "owl.fullname" .)) .Values.tls.secretName }}{{- end -}}

{{- define "owl.relay.image" -}}
{{ .Values.image.relay.repository }}:{{ default .Chart.AppVersion .Values.image.relay.tag }}
{{- end -}}
{{- define "owl.agent.image" -}}
{{ .Values.image.agent.repository }}:{{ default .Chart.AppVersion .Values.image.agent.tag }}
{{- end -}}

{{/* "true" when at least one cluster entry is the local cluster (no kubeconfig). */}}
{{- define "owl.hasLocal" -}}
{{- range .Values.clusters -}}
{{- if and (not .kubeconfig) (not .existingSecret) -}}true{{- end -}}
{{- end -}}
{{- end -}}

{{- define "owl.podSecurity" -}}
runAsNonRoot: true
runAsUser: 65532      # distroless "nonroot"; must be numeric for the kubelet to verify runAsNonRoot
runAsGroup: 65532
fsGroup: 65532        # volumes (the relay's history) are writable by that user
fsGroupChangePolicy: OnRootMismatch
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "owl.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}