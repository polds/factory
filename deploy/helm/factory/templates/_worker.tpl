{{/*
Worker Deployment, ConfigMap, ServiceAccount, and startup-preflight hook.

RBAC rules are copied from warpdotdev/oz-agent-worker
charts/oz-agent-worker/templates/role.yaml at
63dbb8ae8ccb3941392691f5c628e3365d8e248e. The Role is namespace-scoped
in the task namespace. scripts/ci/helm-check.sh diffs the rendered rules
against deploy/helm/factory/tests/upstream/role.yaml.
*/}}

{{- define "factory.workers" -}}
{{- range .Values.workers }}
{{- if .enabled }}
{{- if not .name }}
{{- fail "workers[].name is required" }}
{{- end }}
{{ include "factory.worker.sa" (list $ .) }}
{{ include "factory.worker.role" (list $ .) }}
{{ include "factory.worker.rolebinding" (list $ .) }}
{{ include "factory.worker.configmap" (list $ .) }}
{{ include "factory.worker.deployment" (list $ .) }}
{{ include "factory.worker.service" (list $ .) }}
{{ include "factory.worker.preflight" (list $ .) }}
{{- end }}
{{- end }}
{{- end }}

{{- define "factory.worker.sa" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "factory.workerFullname" (list $root $worker) }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "factory.labels" $root | nindent 4 }}
    app.kubernetes.io/component: worker
    factory.dev/pool: {{ $worker.pool | quote }}
  annotations:
    {{- include "factory.hookAnnotations" (list "-10" "before-hook-creation") | nindent 4 }}
{{- end }}

{{- define "factory.worker.role" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ include "factory.workerFullname" (list $root $worker) }}
  namespace: {{ $worker.kubernetesBackend.namespace }}
  labels:
    {{- include "factory.labels" $root | nindent 4 }}
    app.kubernetes.io/component: worker
    factory.dev/pool: {{ $worker.pool | quote }}
  annotations:
    {{- include "factory.hookAnnotations" (list "-10" "before-hook-creation") | nindent 4 }}
    factory.dev/rbac-provenance: warpdotdev/oz-agent-worker/charts/oz-agent-worker/templates/role.yaml@63dbb8ae8ccb3941392691f5c628e3365d8e248e
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create", "get", "list", "watch", "patch", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["list"]
{{- end }}

{{- define "factory.worker.rolebinding" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "factory.workerFullname" (list $root $worker) }}
  namespace: {{ $worker.kubernetesBackend.namespace }}
  labels:
    {{- include "factory.labels" $root | nindent 4 }}
    app.kubernetes.io/component: worker
    factory.dev/pool: {{ $worker.pool | quote }}
  annotations:
    {{- include "factory.hookAnnotations" (list "-10" "before-hook-creation") | nindent 4 }}
subjects:
  - kind: ServiceAccount
    name: {{ include "factory.workerFullname" (list $root $worker) }}
    namespace: {{ $root.Release.Namespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "factory.workerFullname" (list $root $worker) }}
{{- end }}

{{- define "factory.worker.configmap" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "factory.workerFullname" (list $root $worker) }}-config
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "factory.labels" $root | nindent 4 }}
    app.kubernetes.io/component: worker
    factory.dev/pool: {{ $worker.pool | quote }}
  annotations:
    {{- include "factory.hookAnnotations" (list "-10" "before-hook-creation") | nindent 4 }}
data:
  config.yaml: |
    {{- include "factory.worker.configBody" (list $root $worker) | nindent 4 }}
{{- end }}

{{- define "factory.worker.configBody" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- $kb := $worker.kubernetesBackend -}}
worker_id: {{ required "workers[].workerId is required" $worker.workerId | quote }}
cleanup: {{ $worker.cleanup }}
max_concurrent_tasks: {{ $worker.maxConcurrentTasks }}
collect_task_logs: true
backend:
  kubernetes:
    namespace: {{ $kb.namespace | quote }}
    image_pull_policy: {{ $kb.imagePullPolicy | quote }}
    use_image_volumes: {{ $kb.useImageVolumes }}
    unschedulable_timeout: {{ $kb.unschedulableTimeout | quote }}
    {{- if $kb.defaultImage }}
    default_image: {{ $kb.defaultImage | quote }}
    {{- end }}
    {{- if $kb.preflightImage }}
    preflight_image: {{ $kb.preflightImage | quote }}
    {{- end }}
    {{- if $kb.sidecarImage }}
    sidecar_image: {{ $kb.sidecarImage | quote }}
    {{- end }}
    {{- if $kb.setupCommand }}
    setup_command: |-
      {{- $kb.setupCommand | nindent 6 }}
    {{- end }}
    {{- if $kb.teardownCommand }}
    teardown_command: |-
      {{- $kb.teardownCommand | nindent 6 }}
    {{- end }}
    {{- if $kb.extraLabels }}
    {{- if gt (len $kb.extraLabels) 0 }}
    extra_labels:
      {{- toYaml $kb.extraLabels | nindent 6 }}
    {{- end }}
    {{- end }}
    {{- if $kb.extraAnnotations }}
    {{- if gt (len $kb.extraAnnotations) 0 }}
    extra_annotations:
      {{- toYaml $kb.extraAnnotations | nindent 6 }}
    {{- end }}
    {{- end }}
    {{- if $kb.activeDeadlineSeconds }}
    active_deadline_seconds: {{ $kb.activeDeadlineSeconds }}
    {{- end }}
    {{- if and $worker.cleanup $kb.ttlSecondsAfterFinished }}
    ttl_seconds_after_finished: {{ $kb.ttlSecondsAfterFinished }}
    {{- end }}
    {{- if $kb.workspaceSizeLimit }}
    workspace_size_limit: {{ $kb.workspaceSizeLimit | quote }}
    {{- end }}
    {{- if $kb.podTemplate }}
    {{- if gt (len $kb.podTemplate) 0 }}
    pod_template:
      {{- toYaml $kb.podTemplate | nindent 6 }}
    {{- end }}
    {{- end }}
    {{- if $kb.preflightResources }}
    {{- if gt (len $kb.preflightResources) 0 }}
    preflight_resources:
      {{- toYaml $kb.preflightResources | nindent 6 }}
    {{- end }}
    {{- end }}
{{- end }}

{{- define "factory.worker.deployment" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "factory.workerFullname" (list $root $worker) }}
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "factory.labels" $root | nindent 4 }}
    app.kubernetes.io/component: worker
    factory.dev/pool: {{ $worker.pool | quote }}
spec:
  replicas: {{ $worker.replicas }}
  selector:
    matchLabels:
      {{- include "factory.selector" (list "worker" $root) | nindent 6 }}
      factory.dev/pool: {{ $worker.pool | quote }}
  template:
    metadata:
      labels:
        {{- include "factory.selector" (list "worker" $root) | nindent 8 }}
        factory.dev/pool: {{ $worker.pool | quote }}
      annotations:
        checksum/config: {{ include "factory.worker.configBody" (list $root $worker) | sha256sum }}
    spec:
      terminationGracePeriodSeconds: {{ $worker.terminationGracePeriodSeconds | default 600 }}
      serviceAccountName: {{ include "factory.workerFullname" (list $root $worker) }}
      automountServiceAccountToken: true
      {{- with $root.Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: oz-agent-worker
          image: {{ include "factory.image" (list $root (include "factory.workerRepository" (list $root $worker))) | quote }}
          imagePullPolicy: {{ $root.Values.global.image.pullPolicy }}
          args:
            - --config-file
            - /etc/oz-agent-worker/config.yaml
            - --backend
            - kubernetes
            - --log-level
            - {{ $worker.logLevel | quote }}
            - --web-socket-url
            - {{ include "factory.workerWebSocket" (list $root $worker) | quote }}
            - --server-root-url
            - {{ include "factory.workerServerRoot" (list $root $worker) | quote }}
          env:
            - name: WARP_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.workerApiSecretName" (list $root $worker) }}
                  key: {{ include "factory.workerApiSecretKey" (list $root $worker) }}
            {{- include "factory.worker.metricsEnv" $worker | trim | nindent 12 }}
          {{- if and $worker.metrics.enabled (eq $worker.metrics.exporter "prometheus") }}
          ports:
            - name: metrics
              containerPort: {{ $worker.metrics.port }}
              protocol: TCP
          {{- end }}
          volumeMounts:
            - name: config
              mountPath: /etc/oz-agent-worker
              readOnly: true
          livenessProbe:
            exec:
              command: ["/bin/sh", "-c", "kill -0 1"]
            initialDelaySeconds: 10
            periodSeconds: 30
            failureThreshold: 3
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
          {{- with $worker.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ include "factory.workerFullname" (list $root $worker) }}-config
      {{- if $worker.nodeSelector }}
      {{- if gt (len $worker.nodeSelector) 0 }}
      nodeSelector:
        {{- toYaml $worker.nodeSelector | nindent 8 }}
      {{- end }}
      {{- end }}
      {{- if $worker.affinity }}
      {{- if gt (len $worker.affinity) 0 }}
      affinity:
        {{- toYaml $worker.affinity | nindent 8 }}
      {{- end }}
      {{- end }}
      {{- if $worker.tolerations }}
      {{- if gt (len $worker.tolerations) 0 }}
      tolerations:
        {{- toYaml $worker.tolerations | nindent 8 }}
      {{- end }}
      {{- end }}
{{- end }}

{{- define "factory.worker.metricsEnv" -}}
{{- if .metrics.enabled }}
- name: OTEL_METRICS_EXPORTER
  value: {{ .metrics.exporter | quote }}
{{- if eq .metrics.exporter "prometheus" }}
- name: OTEL_EXPORTER_PROMETHEUS_HOST
  value: "0.0.0.0"
- name: OTEL_EXPORTER_PROMETHEUS_PORT
  value: {{ .metrics.port | quote }}
{{- end }}
{{- else }}
- name: OTEL_METRICS_EXPORTER
  value: "none"
{{- end }}
{{- end }}

{{- define "factory.worker.service" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
{{- if and $worker.metrics.enabled (eq $worker.metrics.exporter "prometheus") }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "factory.workerFullname" (list $root $worker) }}-metrics
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "factory.labels" $root | nindent 4 }}
    app.kubernetes.io/component: worker
    factory.dev/pool: {{ $worker.pool | quote }}
spec:
  type: ClusterIP
  selector:
    {{- include "factory.selector" (list "worker" $root) | nindent 4 }}
    factory.dev/pool: {{ $worker.pool | quote }}
  ports:
    - name: metrics
      port: {{ $worker.metrics.port }}
      targetPort: metrics
      protocol: TCP
{{- end }}
{{- end }}

{{- define "factory.worker.preflight" -}}
{{- $root := index . 0 -}}
{{- $worker := index . 1 -}}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "factory.workerFullname" (list $root $worker) }}-preflight
  namespace: {{ $root.Release.Namespace }}
  labels:
    {{- include "factory.labels" $root | nindent 4 }}
    app.kubernetes.io/component: preflight
    factory.dev/pool: {{ $worker.pool | quote }}
  annotations:
    {{- include "factory.hookAnnotations" (list "5" "before-hook-creation") | nindent 4 }}
spec:
  backoffLimit: 2
  activeDeadlineSeconds: 180
  ttlSecondsAfterFinished: 86400
  template:
    metadata:
      labels:
        {{- include "factory.selector" (list "preflight" $root) | nindent 8 }}
        factory.dev/pool: {{ $worker.pool | quote }}
    spec:
      restartPolicy: Never
      serviceAccountName: {{ include "factory.workerFullname" (list $root $worker) }}
      automountServiceAccountToken: true
      terminationGracePeriodSeconds: 30
      {{- with $root.Values.global.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: preflight
          image: {{ include "factory.image" (list $root (include "factory.workerRepository" (list $root $worker))) | quote }}
          imagePullPolicy: {{ $root.Values.global.image.pullPolicy }}
          command: ["/bin/sh", "-c"]
          args:
            - |
              set -eu
              log=/tmp/worker.log
              /oz-agent-worker \
                --config-file /etc/oz-agent-worker/config.yaml \
                --backend kubernetes \
                --log-level "$PREFLIGHT_LOG_LEVEL" \
                --web-socket-url "$PREFLIGHT_WS" \
                --server-root-url "$PREFLIGHT_ROOT" \
                >"$log" 2>&1 &
              pid=$!
              i=0
              while [ "$i" -lt 90 ]; do
                if grep -q "Connecting to" "$log"; then
                  cat "$log"
                  kill "$pid" 2>/dev/null || true
                  wait "$pid" 2>/dev/null || true
                  exit 0
                fi
                if ! kill -0 "$pid" 2>/dev/null; then
                  cat "$log" >&2
                  exit 1
                fi
                i=$((i + 1))
                sleep 1
              done
              cat "$log" >&2
              kill "$pid" 2>/dev/null || true
              exit 1
          env:
            - name: PREFLIGHT_LOG_LEVEL
              value: {{ $worker.logLevel | quote }}
            - name: PREFLIGHT_WS
              value: {{ include "factory.workerWebSocket" (list $root $worker) | quote }}
            - name: PREFLIGHT_ROOT
              value: {{ include "factory.workerServerRoot" (list $root $worker) | quote }}
            - name: WARP_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.workerApiSecretName" (list $root $worker) }}
                  key: {{ include "factory.workerApiSecretKey" (list $root $worker) }}
            - name: OTEL_METRICS_EXPORTER
              value: "none"
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
          volumeMounts:
            - name: config
              mountPath: /etc/oz-agent-worker
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: {{ include "factory.workerFullname" (list $root $worker) }}-config
{{- end }}
