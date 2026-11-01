{{- define "factory.dev" -}}
{{- if .Values.postgresql.enabled }}
---
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "factory.dbSecretName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: postgresql
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-30" "before-hook-creation") | nindent 4 }}
type: Opaque
stringData:
  host: {{ include "factory.postgresHost" . | quote }}
  port: "5432"
  user: {{ .Values.postgresql.auth.username | quote }}
  password: {{ .Values.postgresql.auth.password | quote }}
  dbname: {{ .Values.postgresql.auth.database | quote }}
  url: {{ printf "postgres://%s:%s@%s:5432/%s?sslmode=disable" (.Values.postgresql.auth.username | urlquery) (.Values.postgresql.auth.password | urlquery) (include "factory.postgresHost" .) (.Values.postgresql.auth.database | urlquery) | quote }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "factory.postgresHost" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: postgresql
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-20" "before-hook-creation") | nindent 4 }}
spec:
  clusterIP: None
  selector:
    {{- include "factory.selector" (list "postgresql" .) | nindent 4 }}
  ports:
    - name: postgres
      port: 5432
      targetPort: postgres
      protocol: TCP
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ include "factory.postgresHost" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: postgresql
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-20" "before-hook-creation") | nindent 4 }}
spec:
  serviceName: {{ include "factory.postgresHost" . }}
  replicas: 1
  selector:
    matchLabels:
      {{- include "factory.selector" (list "postgresql" .) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "factory.labels" . | nindent 8 }}
        app.kubernetes.io/component: postgresql
        factory.dev/dev-eval: "true"
    spec:
      automountServiceAccountToken: false
      {{- with .Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: postgres
          image: {{ include "factory.pinnedImage" .Values.postgresql.image | quote }}
          imagePullPolicy: IfNotPresent
          ports:
            - name: postgres
              containerPort: 5432
              protocol: TCP
          env:
            - name: POSTGRES_USER
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.dbSecretName" . }}
                  key: user
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.dbSecretName" . }}
                  key: password
            - name: POSTGRES_DB
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.dbSecretName" . }}
                  key: dbname
            - name: PGDATA
              value: /var/lib/postgresql/data/pgdata
          readinessProbe:
            exec:
              command:
                - pg_isready
                - -U
                - {{ .Values.postgresql.auth.username | quote }}
                - -d
                - {{ .Values.postgresql.auth.database | quote }}
            initialDelaySeconds: 5
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 30
          {{- with .Values.postgresql.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: data
              mountPath: /var/lib/postgresql/data
  volumeClaimTemplates:
    - metadata:
        name: data
        labels:
          factory.dev/dev-eval: "true"
      spec:
        accessModes:
          - ReadWriteOnce
        resources:
          requests:
            storage: {{ .Values.postgresql.persistence.size }}
{{- end }}
{{- if .Values.minio.enabled }}
---
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "factory.s3SecretName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: minio
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-30" "before-hook-creation") | nindent 4 }}
type: Opaque
stringData:
  accessKey: {{ .Values.minio.auth.rootUser | quote }}
  secretKey: {{ .Values.minio.auth.rootPassword | quote }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "factory.minioHost" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: minio
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-20" "before-hook-creation") | nindent 4 }}
spec:
  clusterIP: None
  selector:
    {{- include "factory.selector" (list "minio" .) | nindent 4 }}
  ports:
    - name: minio
      port: 9000
      targetPort: minio
      protocol: TCP
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ include "factory.minioHost" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: minio
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-20" "before-hook-creation") | nindent 4 }}
spec:
  serviceName: {{ include "factory.minioHost" . }}
  replicas: 1
  selector:
    matchLabels:
      {{- include "factory.selector" (list "minio" .) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "factory.labels" . | nindent 8 }}
        app.kubernetes.io/component: minio
        factory.dev/dev-eval: "true"
    spec:
      automountServiceAccountToken: false
      {{- with .Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: minio
          image: {{ include "factory.pinnedImage" .Values.minio.image | quote }}
          imagePullPolicy: IfNotPresent
          ports:
            - name: minio
              containerPort: 9000
              protocol: TCP
          env:
            - name: MINIO_ROOT_USER
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.s3SecretName" . }}
                  key: accessKey
            - name: MINIO_ROOT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.s3SecretName" . }}
                  key: secretKey
          readinessProbe:
            exec:
              command:
                - bash
                - -c
                - curl -fsS http://127.0.0.1:9000/minio/health/ready >/dev/null && test "$(cat /proc/1/comm)" = minio
            initialDelaySeconds: 10
            periodSeconds: 5
            timeoutSeconds: 5
            failureThreshold: 30
          {{- with .Values.minio.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: data
              mountPath: /bitnami/minio/data
  volumeClaimTemplates:
    - metadata:
        name: data
        labels:
          factory.dev/dev-eval: "true"
      spec:
        accessModes:
          - ReadWriteOnce
        resources:
          requests:
            storage: {{ .Values.minio.persistence.size }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "factory.minioHost" . }}-init
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: minio
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-10" "before-hook-creation,hook-succeeded") | nindent 4 }}
spec:
  backoffLimit: 2
  ttlSecondsAfterFinished: 600
  template:
    metadata:
      labels:
        {{- include "factory.selector" (list "minio-init" .) | nindent 8 }}
        factory.dev/dev-eval: "true"
    spec:
      automountServiceAccountToken: false
      restartPolicy: Never
      {{- with .Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: minio-init
          image: {{ include "factory.pinnedImage" .Values.minio.clientImage | quote }}
          imagePullPolicy: IfNotPresent
          env:
            - name: MINIO_HOST
              value: {{ include "factory.minioHost" . | quote }}
            - name: MINIO_BUCKET
              value: {{ .Values.minio.bucket | quote }}
            - name: MINIO_ROOT_USER
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.s3SecretName" . }}
                  key: accessKey
            - name: MINIO_ROOT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.s3SecretName" . }}
                  key: secretKey
          command: ["/bin/bash", "-ec"]
          args:
            - |
              mkdir -p /tmp/mc
              ok=0
              i=0
              while [ "$i" -lt 60 ]; do
                if mc --config-dir /tmp/mc alias set local "http://${MINIO_HOST}:9000" \
                  "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" \
                  && mc --config-dir /tmp/mc mb --ignore-existing "local/${MINIO_BUCKET}" \
                  && mc --config-dir /tmp/mc anonymous set none "local/${MINIO_BUCKET}"; then
                  ok=1
                  break
                fi
                i=$((i + 1))
                sleep 2
              done
              if [ "$ok" -ne 1 ]; then
                echo "minio did not accept bucket setup" >&2
                exit 1
              fi
{{- end }}
{{- end }}
