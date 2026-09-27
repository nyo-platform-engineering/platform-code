{{- define "telemetry-ui.name" -}}
{{ default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end -}}
{{- define "telemetry-ui.fullname" -}}
{{ default (printf "%s-%s" .Release.Name (include "telemetry-ui.name" .)) .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- end -}}
{{- define "telemetry-ui.selectorLabels" -}}
app.kubernetes.io/name: {{ include "telemetry-ui.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "telemetry-ui.labels" -}}
{{ include "telemetry-ui.selectorLabels" . }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "telemetry-ui.image" -}}
{{- if .Values.image.digest -}}
{{ printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else -}}
{{ printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end -}}
{{- end -}}
{{- define "telemetry-ui.connectionEnv" -}}
{{- $prefix := .prefix -}}
{{- $address := required (printf "%sADDR requires address" $prefix) .config.address -}}
{{- $username := required (printf "%sUSER requires username" $prefix) .config.username -}}
{{- $secret := required (printf "%sPASSWORD requires passwordSecret" $prefix) .config.passwordSecret -}}
{{- with $address }}
- name: {{ $prefix }}ADDR
  value: {{ . | quote }}
{{- end }}
{{- with $username }}
- name: {{ $prefix }}USER
  value: {{ . | quote }}
{{- end }}
{{- with $secret }}
- name: {{ $prefix }}PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ required (printf "%sPASSWORD requires passwordSecret.name" $prefix) .name | quote }}
      key: {{ required (printf "%sPASSWORD requires passwordSecret.key" $prefix) .key | quote }}
{{- end }}
{{- end -}}
