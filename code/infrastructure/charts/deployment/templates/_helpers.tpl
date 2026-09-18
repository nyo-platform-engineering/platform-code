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
{{- $labels := deepCopy (.Values.labels | default dict) -}}
{{- $fixed := include "deployment.selectorLabels" . | fromYaml -}}
{{- $_ := set $fixed "app.kubernetes.io/managed-by" .Release.Service -}}
{{- $_ := set $fixed "helm.sh/chart" (printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_") -}}
{{- mergeOverwrite $labels $fixed | toYaml -}}
{{- end -}}

{{- define "deployment.podLabels" -}}
{{- $labels := include "deployment.labels" . | fromYaml -}}
{{- $overrides := deepCopy (.Values.podLabels | default dict) -}}
{{- $selectors := include "deployment.selectorLabels" . | fromYaml -}}
{{- mergeOverwrite $labels $overrides $selectors | toYaml -}}
{{- end -}}

{{- define "deployment.image" -}}
{{- $repository := required "image.repository is required" .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{ printf "%s@%s" $repository .Values.image.digest }}
{{- else -}}
{{ printf "%s:%s" $repository (required "image.tag or image.digest is required" .Values.image.tag) }}
{{- end -}}
{{- end -}}
