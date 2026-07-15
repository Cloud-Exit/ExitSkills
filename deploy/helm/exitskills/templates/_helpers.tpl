{{- define "exitskills.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "exitskills.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ include "exitskills.name" . }}{{ end }}
{{- end }}
{{- define "exitskills.labels" -}}
app.kubernetes.io/name: {{ include "exitskills.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}
{{- define "exitskills.selectorLabels" -}}
app.kubernetes.io/name: {{ include "exitskills.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "exitskills.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}{{ default (include "exitskills.fullname" .) .Values.serviceAccount.name }}{{ else }}{{ default "default" .Values.serviceAccount.name }}{{ end }}
{{- end }}
{{- define "exitskills.secretName" -}}
{{- default (include "exitskills.fullname" .) .Values.existingSecret }}
{{- end }}
