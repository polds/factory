{{- define "factory.factorySecrets" -}}
{{- $boot := .Values.controlPlane.bootstrapSecrets | default dict -}}
{{- $known := .Values.controlPlane.factorySecrets | default dict -}}
{{- if or (gt (len $boot) 0) (gt (len $known) 0) }}
---
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "factory.fullname" . }}-factory-secrets
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
type: Opaque
stringData:
{{- range $name, $value := $boot }}
{{- if not (regexMatch "^[A-Z][A-Z0-9_]*$" $name) }}
{{- fail (printf "bootstrap secret name %s must match [A-Z][A-Z0-9_]*" $name) }}
{{- end }}
  bootstrap.{{ $name }}: {{ $value | quote }}
{{- end }}
{{- range $id, $secrets := $known }}
{{- if or (contains "/" (printf "%s" $id)) (not (regexMatch "^[-._a-zA-Z0-9]+$" (printf "%s" $id))) }}
{{- fail (printf "factory id %s is not a valid Secret key" $id) }}
{{- end }}
{{- range $name, $value := $secrets }}
{{- if or (contains "/" (printf "%s" $name)) (not (regexMatch "^[A-Z][A-Z0-9_]*$" (printf "%s" $name))) }}
{{- fail (printf "factory secret name %s must match [A-Z][A-Z0-9_]*" $name) }}
{{- end }}
  {{ $id }}.{{ $name }}: {{ $value | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
