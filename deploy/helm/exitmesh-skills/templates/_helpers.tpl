{{- define "exitmesh-skills.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "exitmesh-skills.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ include "exitmesh-skills.name" . }}{{ end }}
{{- end }}
{{- define "exitmesh-skills.labels" -}}
app.kubernetes.io/name: {{ include "exitmesh-skills.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}
{{- define "exitmesh-skills.selectorLabels" -}}
app.kubernetes.io/name: {{ include "exitmesh-skills.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "exitmesh-skills.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}{{ default (include "exitmesh-skills.fullname" .) .Values.serviceAccount.name }}{{ else }}{{ default "default" .Values.serviceAccount.name }}{{ end }}
{{- end }}
{{- define "exitmesh-skills.secretName" -}}
{{- default (include "exitmesh-skills.fullname" .) .Values.existingSecret }}
{{- end }}

