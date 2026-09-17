{{- define "deployment.name" -}}
{{- default .Release.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "deployment.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "deployment.selectorLabels" -}}
app.kubernetes.io/name: {{ include "deployment.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "deployment.labels" -}}
{{- with .Values.labels }}
{{ toYaml . }}
{{- end }}
{{ include "deployment.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "deployment.image" -}}
{{- $repository := required "image.repository is required" .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{ printf "%s@%s" $repository .Values.image.digest }}
{{- else -}}
{{ printf "%s:%s" $repository (required "image.tag or image.digest is required" .Values.image.tag) }}
{{- end -}}
{{- end -}}
