{{- define "factory.controlplane" -}}
{{- if and .Values.operator.enabled (or (gt (int .Values.controlPlane.replicas) 1) .Values.controlPlane.autoscaling.enabled) -}}
{{- fail "operator.enabled needs controlPlane.replicas=1 (API-backed definitions are per pod)" -}}
{{- end -}}
{{- if .Values.controlPlane.enabled }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "factory.factorydName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
  {{- with .Values.controlPlane.serviceAccount.annotations }}
  {{- if gt (len .) 0 }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- end }}
automountServiceAccountToken: false
{{- if not .Values.controlPlane.definition.existingConfigMap }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "factory.definitionName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
data:
  {{- range $name, $body := .Values.controlPlane.definition.files }}
  {{ $name }}: |
    {{- $body | nindent 4 }}
  {{- end }}
{{- end }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "factory.factorydName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
spec:
  type: ClusterIP
  selector:
    {{- include "factory.selector" (list "control-plane" .) | nindent 4 }}
  ports:
    - name: http
      port: 8080
      targetPort: http
      protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "factory.factorydName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
spec:
  {{- if not .Values.controlPlane.autoscaling.enabled }}
  replicas: {{ .Values.controlPlane.replicas }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "factory.selector" (list "control-plane" .) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "factory.labels" . | nindent 8 }}
        app.kubernetes.io/component: control-plane
      annotations:
        checksum/definition: {{ include "factory.definitionChecksum" . | sha256sum }}
    spec:
      serviceAccountName: {{ include "factory.factorydName" . }}
      automountServiceAccountToken: false
      terminationGracePeriodSeconds: 30
      {{- with .Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: factoryd
          image: {{ include "factory.image" (list . .Values.controlPlane.image.repository) | quote }}
          imagePullPolicy: {{ .Values.global.image.pullPolicy }}
          args: ["serve"]
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
          env:
            - name: FACTORY_HTTP_ADDR
              value: ":8080"
            - name: FACTORY_EXTERNAL_URL
              value: {{ include "factory.externalURL" . | quote }}
            - name: FACTORY_TASK_SERVER_URL
              value: {{ include "factory.taskServerURL" . | quote }}
            - name: FACTORY_DEFINITION_PATH
              value: {{ .Values.controlPlane.definition.mountPath | quote }}
            {{- if .Values.operator.enabled }}
            - name: FACTORY_DEFINITION_SOURCE
              value: api
            {{- end }}
            - name: FACTORY_LOG_LEVEL
              value: info
            - name: FACTORY_WORKER_POOL
              value: {{ include "factory.primaryPool" . | quote }}
            - name: FACTORY_WORKER_PLATFORM
              value: linux
            - name: FACTORY_WORKER_MAX_CONCURRENT
              value: {{ include "factory.primaryMaxConcurrent" . | quote }}
            - name: FACTORY_DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.dbSecretName" . }}
                  key: {{ include "factory.dbSecretKey" . }}
            - name: FACTORY_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.apiSecretName" . }}
                  key: {{ .Values.controlPlane.api.apiKeyKey }}
            - name: FACTORY_WORKER_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.apiSecretName" . }}
                  key: {{ .Values.controlPlane.api.workerKeyKey }}
            {{- include "factory.s3Env" . | trim | nindent 12 }}
            {{- with .Values.controlPlane.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- if .Values.controlPlane.secretProjection.enabled }}
            - name: FACTORY_SECRET_MOUNT_DIR
              value: {{ .Values.controlPlane.secretProjection.mountPath | quote }}
            - name: FACTORY_SECRET_ALLOWLIST
              value: {{ include "factory.secretProjectionAllowlist" . | quote }}
            {{- end }}
          {{- with .Values.controlPlane.extraEnvFrom }}
          envFrom:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          readinessProbe:
            exec:
              command: ["/factoryd", "healthcheck"]
            initialDelaySeconds: 2
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 30
          livenessProbe:
            exec:
              command: ["/factoryd", "healthcheck"]
            initialDelaySeconds: 15
            periodSeconds: 10
            timeoutSeconds: 3
            failureThreshold: 6
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
          {{- with .Values.controlPlane.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            {{- if .Values.operator.enabled }}
            - name: definition
              mountPath: {{ dir .Values.controlPlane.definition.mountPath }}
            {{- else }}
            {{- range $name, $_ := .Values.controlPlane.definition.files }}
            - name: definition
              mountPath: {{ $.Values.controlPlane.definition.mountPath }}/{{ $name }}
              subPath: {{ $name }}
              readOnly: true
            {{- end }}
            {{- end }}
            {{- with .Values.controlPlane.extraVolumeMounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- if .Values.controlPlane.secretProjection.enabled }}
            - name: projected-secrets
              mountPath: {{ .Values.controlPlane.secretProjection.mountPath | quote }}
              readOnly: true
            {{- end }}
      volumes:
        {{- if .Values.operator.enabled }}
        - name: definition
          emptyDir: {}
        {{- else }}
        - name: definition
          configMap:
            name: {{ include "factory.definitionName" . }}
        {{- end }}
        {{- with .Values.controlPlane.extraVolumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        {{- if .Values.controlPlane.secretProjection.enabled }}
        - name: projected-secrets
          projected:
            defaultMode: 384
            sources:
            {{- range .Values.controlPlane.secretProjection.sources }}
              - secret:
                  name: {{ required "secretProjection.sources[].secretName" .secretName | quote }}
                  items:
                  {{- range .items }}
                    - key: {{ required "key" .key | quote }}
                      path: {{ required "path" .path | quote }}
                  {{- end }}
            {{- end }}
        {{- end }}
{{- if .Values.controlPlane.ingress.enabled }}
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ include "factory.factorydName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
spec:
  {{- with .Values.controlPlane.ingress.className }}
  ingressClassName: {{ . }}
  {{- end }}
  {{- with .Values.controlPlane.ingress.tls.secretName }}
  tls:
    - hosts:
        - {{ $.Values.controlPlane.ingress.host | quote }}
      secretName: {{ . }}
  {{- end }}
  rules:
    - host: {{ .Values.controlPlane.ingress.host | quote }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ include "factory.factorydName" . }}
                port:
                  name: http
{{- end }}
{{- if .Values.controlPlane.autoscaling.enabled }}
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "factory.factorydName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "factory.factorydName" . }}
  minReplicas: {{ .Values.controlPlane.autoscaling.minReplicas }}
  maxReplicas: {{ .Values.controlPlane.autoscaling.maxReplicas }}
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: {{ .Values.controlPlane.autoscaling.targetCPUUtilizationPercentage }}
{{- end }}
{{- end }}
{{- end }}

{{- define "factory.definitionChecksum" -}}
{{- if .Values.controlPlane.definition.existingConfigMap -}}
existing
{{- else -}}
{{- toYaml .Values.controlPlane.definition.files -}}
{{- end -}}
{{- end -}}

{{- define "factory.primaryWorker" -}}
{{- $found := dict "worker" dict -}}
{{- range .Values.workers -}}
{{- if and .enabled (not (index $found "ok")) -}}
{{- $_ := set $found "worker" . -}}
{{- $_ := set $found "ok" true -}}
{{- end -}}
{{- end -}}
{{- if not (index $found "ok") -}}
{{- fail "at least one enabled worker is required" -}}
{{- end -}}
{{- toJson (index $found "worker") -}}
{{- end -}}

{{- define "factory.primaryPool" -}}
{{- $w := include "factory.primaryWorker" . | fromJson -}}
{{- $w.pool | default "standard" -}}
{{- end -}}

{{- define "factory.primaryMaxConcurrent" -}}
{{- $w := include "factory.primaryWorker" . | fromJson -}}
{{- $w.maxConcurrentTasks | default 1 -}}
{{- end -}}

{{- define "factory.s3Env" -}}
{{- if .Values.minio.enabled }}
- name: FACTORY_S3_ENDPOINT
  value: {{ printf "http://%s:9000" (include "factory.minioHost" .) | quote }}
- name: FACTORY_S3_BUCKET
  value: {{ .Values.minio.bucket | quote }}
- name: FACTORY_S3_REGION
  value: us-east-1
- name: FACTORY_S3_FORCE_PATH_STYLE
  value: "true"
- name: FACTORY_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "factory.s3SecretName" . }}
      key: accessKey
- name: FACTORY_S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "factory.s3SecretName" . }}
      key: secretKey
{{- else if not .Values.controlPlane.objectStore.irsa }}
- name: FACTORY_S3_ENDPOINT
  value: {{ .Values.controlPlane.objectStore.endpoint | quote }}
- name: FACTORY_S3_BUCKET
  value: {{ .Values.controlPlane.objectStore.bucket | quote }}
- name: FACTORY_S3_REGION
  value: {{ .Values.controlPlane.objectStore.region | quote }}
- name: FACTORY_S3_FORCE_PATH_STYLE
  value: {{ .Values.controlPlane.objectStore.forcePathStyle | quote }}
- name: FACTORY_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "factory.s3SecretName" . }}
      key: {{ .Values.controlPlane.objectStore.accessKeyKey }}
- name: FACTORY_S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "factory.s3SecretName" . }}
      key: {{ .Values.controlPlane.objectStore.secretKeyKey }}
{{- else }}
- name: FACTORY_S3_ENDPOINT
  value: {{ .Values.controlPlane.objectStore.endpoint | quote }}
- name: FACTORY_S3_BUCKET
  value: {{ .Values.controlPlane.objectStore.bucket | quote }}
- name: FACTORY_S3_REGION
  value: {{ .Values.controlPlane.objectStore.region | quote }}
- name: FACTORY_S3_FORCE_PATH_STYLE
  value: {{ .Values.controlPlane.objectStore.forcePathStyle | quote }}
{{- end }}
{{- end }}

{{- define "factory.apiSecret" -}}
{{- if .Values.controlPlane.api.create }}
{{- if or (not .Values.controlPlane.api.apiKey) (not .Values.controlPlane.api.workerKey) }}
{{- fail "controlPlane.api.apiKey and controlPlane.api.workerKey are required when controlPlane.api.create is true" }}
{{- end }}
---
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "factory.apiSecretName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: control-plane
    factory.dev/dev-eval: "true"
  annotations:
    {{- include "factory.hookAnnotations" (list "-30" "before-hook-creation") | nindent 4 }}
type: Opaque
stringData:
  {{ .Values.controlPlane.api.apiKeyKey }}: {{ .Values.controlPlane.api.apiKey | quote }}
  {{ .Values.controlPlane.api.workerKeyKey }}: {{ .Values.controlPlane.api.workerKey | quote }}
{{- end }}
{{- end }}
