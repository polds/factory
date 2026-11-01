{{/*
Chart-only helpers. Worker RBAC rules are not defined here; they live in
_worker.tpl and match warpdotdev/oz-agent-worker charts/oz-agent-worker
templates/role.yaml at 63dbb8ae8ccb3941392691f5c628e3365d8e248e.
*/}}

{{- define "factory.validate" -}}
{{- $tag := .Values.global.image.tag | toString | trim -}}
{{- if or (eq $tag "") (eq $tag "latest") -}}
{{- fail "global.image.tag is required and must be pinned" -}}
{{- end -}}
{{- end -}}

{{- define "factory.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "factory.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "factory.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "factory.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "factory.labels" -}}
helm.sh/chart: {{ include "factory.chart" . }}
app.kubernetes.io/name: {{ include "factory.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: factory
{{- end -}}

{{- define "factory.selector" -}}
{{- $component := index . 0 -}}
{{- $root := index . 1 -}}
app.kubernetes.io/name: {{ include "factory.name" $root }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/component: {{ $component }}
{{- end -}}

{{- define "factory.factorydName" -}}
{{- printf "%s-factoryd" (include "factory.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "factory.workerFullname" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- printf "%s-worker-%s" (include "factory.fullname" $root) $worker.name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "factory.hookAnnotations" -}}
{{- $weight := index . 0 -}}
{{- $policy := index . 1 -}}
helm.sh/hook: pre-install,pre-upgrade
helm.sh/hook-weight: {{ $weight | quote }}
helm.sh/hook-delete-policy: {{ $policy }}
{{- end -}}

{{- define "factory.image" -}}
{{- $root := index . 0 -}}
{{- $repo := index . 1 | toString | trim -}}
{{- if not $repo -}}
{{- fail "image repository is required" -}}
{{- end -}}
{{- $tag := $root.Values.global.image.tag | toString | trim -}}
{{- if or (eq $tag "") (eq $tag "latest") -}}
{{- fail "global.image.tag is required and must be pinned" -}}
{{- end -}}
{{- $reg := $root.Values.global.image.registry | toString | trim -}}
{{- if $reg -}}
{{- printf "%s/%s:%s" $reg $repo $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{- define "factory.pinnedImage" -}}
{{- $digest := .digest | toString | trim -}}
{{- if or (not $digest) (not .repository) (not .tag) -}}
{{- fail (printf "pinned image requires repository, tag, and digest (repository %q)" (.repository | default "")) -}}
{{- end -}}
{{- printf "%s:%s@%s" .repository .tag $digest -}}
{{- end -}}

{{- define "factory.workerRepository" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- if and $worker.image $worker.image.repository -}}
{{- $worker.image.repository -}}
{{- else -}}
{{- $root.Values.worker.image.repository -}}
{{- end -}}
{{- end -}}

{{- define "factory.apiSecretName" -}}
{{- if .Values.controlPlane.api.create -}}
{{- printf "%s-api" (include "factory.fullname" .) -}}
{{- else -}}
{{- required "controlPlane.api.existingSecret is required when controlPlane.api.create is false" .Values.controlPlane.api.existingSecret -}}
{{- end -}}
{{- end -}}

{{- define "factory.workerApiSecretName" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- if $root.Values.controlPlane.api.create -}}
{{- include "factory.apiSecretName" $root -}}
{{- else -}}
{{- required "workers[].apiKeySecret.name is required when controlPlane.api.create is false" $worker.apiKeySecret.name -}}
{{- end -}}
{{- end -}}

{{- define "factory.workerApiSecretKey" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- if $root.Values.controlPlane.api.create -}}
{{- $root.Values.controlPlane.api.workerKeyKey -}}
{{- else -}}
{{- $worker.apiKeySecret.key | default "FACTORY_API_KEY" -}}
{{- end -}}
{{- end -}}

{{- define "factory.dbSecretName" -}}
{{- if .Values.postgresql.enabled -}}
{{- printf "%s-postgresql" (include "factory.fullname" .) -}}
{{- else -}}
{{- required "controlPlane.database.existingSecret is required when postgresql.enabled is false" .Values.controlPlane.database.existingSecret -}}
{{- end -}}
{{- end -}}

{{- define "factory.dbSecretKey" -}}
{{- if .Values.postgresql.enabled -}}
url
{{- else -}}
{{- .Values.controlPlane.database.urlKey | default "url" -}}
{{- end -}}
{{- end -}}

{{- define "factory.s3SecretName" -}}
{{- if .Values.minio.enabled -}}
{{- printf "%s-minio" (include "factory.fullname" .) -}}
{{- else -}}
{{- required "controlPlane.objectStore.existingSecret is required when minio.enabled is false" .Values.controlPlane.objectStore.existingSecret -}}
{{- end -}}
{{- end -}}

{{- define "factory.definitionName" -}}
{{- if .Values.controlPlane.definition.existingConfigMap -}}
{{- .Values.controlPlane.definition.existingConfigMap -}}
{{- else -}}
{{- printf "%s-definition" (include "factory.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "factory.externalURL" -}}
{{- if .Values.controlPlane.externalUrl -}}
{{- .Values.controlPlane.externalUrl -}}
{{- else -}}
{{- printf "http://%s.%s.svc:8080" (include "factory.factorydName" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "factory.taskServerURL" -}}
{{- if .Values.controlPlane.taskServerURL -}}
{{- .Values.controlPlane.taskServerURL -}}
{{- else -}}
{{- printf "http://%s.%s.svc:8080" (include "factory.factorydName" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "factory.workerWebSocket" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- if $worker.webSocketURL -}}
{{- $worker.webSocketURL -}}
{{- else -}}
{{- printf "ws://%s.%s.svc:8080/api/v1/selfhosted/worker/ws" (include "factory.factorydName" $root) $root.Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "factory.workerServerRoot" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- if $worker.serverRootURL -}}
{{- $worker.serverRootURL -}}
{{- else -}}
{{- printf "http://%s.%s.svc:8080" (include "factory.factorydName" $root) $root.Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "factory.postgresHost" -}}
{{- printf "%s-postgresql" (include "factory.fullname" .) -}}
{{- end -}}

{{- define "factory.minioHost" -}}
{{- printf "%s-minio" (include "factory.fullname" .) -}}
{{- end -}}
