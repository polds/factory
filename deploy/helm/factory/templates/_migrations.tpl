{{- define "factory.migrations" -}}
{{- if .Values.controlPlane.enabled }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "factory.fullname" . }}-migrate
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: migration
  annotations:
    {{- include "factory.hookAnnotations" (list "0" "before-hook-creation") | nindent 4 }}
spec:
  backoffLimit: 6
  activeDeadlineSeconds: 600
  ttlSecondsAfterFinished: 86400
  template:
    metadata:
      labels:
        {{- include "factory.selector" (list "migration" .) | nindent 8 }}
    spec:
      automountServiceAccountToken: false
      restartPolicy: OnFailure
      {{- with .Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- if .Values.postgresql.enabled }}
      initContainers:
        - name: wait-for-postgres
          image: {{ include "factory.pinnedImage" .Values.postgresql.image | quote }}
          imagePullPolicy: IfNotPresent
          command:
            - sh
            - -c
            - until pg_isready -h "$PGHOST" -p 5432 -U "$PGUSER" -d "$PGDATABASE"; do sleep 2; done
          env:
            - name: PGHOST
              value: {{ include "factory.postgresHost" . | quote }}
            - name: PGUSER
              value: {{ .Values.postgresql.auth.username | quote }}
            - name: PGDATABASE
              value: {{ .Values.postgresql.auth.database | quote }}
      {{- end }}
      containers:
        - name: migrate
          image: {{ include "factory.image" (list . .Values.controlPlane.image.repository) | quote }}
          imagePullPolicy: {{ .Values.global.image.pullPolicy }}
          args: ["migrate", "up"]
          env:
            - name: FACTORY_DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.dbSecretName" . }}
                  key: {{ include "factory.dbSecretKey" . }}
          securityContext:
            runAsNonRoot: true
            runAsUser: 65532
            runAsGroup: 65532
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
{{- end }}
{{- end }}
