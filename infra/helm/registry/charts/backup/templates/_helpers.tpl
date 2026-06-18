{{/*
Common name helpers for the backup subchart. Mirrors the per-service
charts' helpers so labels render consistently across the umbrella release.
*/}}
{{- define "backup.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "backup.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "registry-%s" .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "backup.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "backup.labels" -}}
helm.sh/chart: {{ include "backup.chart" . }}
{{ include "backup.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: backup
{{- end }}

{{- define "backup.selectorLabels" -}}
app.kubernetes.io/name: {{ include "backup.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "backup.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "backup.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "backup.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Common env vars passed to every backup CronJob — AWS / S3 plumbing for the
target bucket. Kept here so a target-config change is one edit, not 9.
*/}}
{{- define "backup.targetEnv" -}}
- name: AWS_DEFAULT_REGION
  value: {{ .Values.target.region | quote }}
- name: AWS_REGION
  value: {{ .Values.target.region | quote }}
{{- if .Values.target.endpointURL }}
- name: AWS_ENDPOINT_URL
  value: {{ .Values.target.endpointURL | quote }}
{{- end }}
{{- if .Values.target.forcePathStyle }}
- name: AWS_S3_FORCE_PATH_STYLE
  value: "true"
{{- end }}
- name: BACKUP_BUCKET
  value: {{ .Values.target.bucket | quote }}
- name: BACKUP_ENV
  value: {{ .Values.target.environment | quote }}
- name: BACKUP_SSE
  value: {{ .Values.target.sseAlgorithm | quote }}
{{- end }}

{{/*
Common pod-level security context. Read-only root FS forces the scripts to
write only to /tmp (emptyDir) so a compromised job can't persist anything.
*/}}
{{- define "backup.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "backup.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end }}
