{{- define "factory.operatorName" -}}
{{- printf "%s-operator" (include "factory.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "factory.operatorWebhookName" -}}
{{- printf "%s-webhook" (include "factory.operatorName" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "factory.operatorValidatingName" -}}
{{- printf "%s-validating" (include "factory.operatorName" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "factory.operatorNamespaces" -}}
{{- if .Values.operator.watchNamespaces }}
{{- join "," .Values.operator.watchNamespaces }}
{{- else }}
{{- .Release.Namespace }}
{{- end }}
{{- end -}}

{{- define "factory.operatorTaskNamespaces" -}}
{{- $ns := .Values.operator.workerPools.taskNamespaces -}}
{{- if not $ns -}}
{{- $ns = list "factory-runs" -}}
{{- end -}}
{{- join "," $ns -}}
{{- end -}}

{{- define "factory.operatorPullSecrets" -}}
{{- $names := list -}}
{{- range .Values.global.imagePullSecrets -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}

{{- define "factory.operatorRules" -}}
rules:
  - apiGroups: ["factory.dev"]
    resources:
      - factories
      - factoryagents
      - automations
      - runners
      - modelrouters
      - scorers
      - skills
      - factorywebhooks
      - benchmarksuites
      - agentruns
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["factory.dev"]
    resources:
      - factories/status
      - factoryagents/status
      - automations/status
      - runners/status
      - modelrouters/status
      - scorers/status
      - skills/status
      - factorywebhooks/status
      - benchmarksuites/status
      - agentruns/status
    verbs: ["get", "update", "patch"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["secrets"]
    resourceNames: [{{ include "factory.operatorWebhookName" . | quote }}]
    verbs: ["get", "update", "patch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
  - apiGroups: ["factory.dev"]
    resources: ["workerpools"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["factory.dev"]
    resources: ["workerpools/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: ["factory.dev"]
    resources: ["workerpools/finalizers"]
    verbs: ["update"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["create", "get", "list", "watch", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["configmaps", "serviceaccounts"]
    verbs: ["create", "get", "list", "watch", "update", "patch", "delete"]
{{- end -}}

{{/*
installCRDs does not gate Helm's crds/ directory. Helm installs those
CRDs on every release. The value is retained for T92 to record.
*/}}
{{- define "factory.operator" -}}
{{- if .Values.operator.enabled }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "factory.operatorName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
{{- $namespaces := .Values.operator.watchNamespaces }}
{{- if not $namespaces }}
{{- $namespaces = list .Release.Namespace }}
{{- end }}
{{- range $ns := $namespaces }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ include "factory.operatorName" $ }}
  namespace: {{ $ns }}
  labels:
    {{- include "factory.labels" $ | nindent 4 }}
    app.kubernetes.io/component: operator
{{ include "factory.operatorRules" $ | nindent 0 }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "factory.operatorName" $ }}
  namespace: {{ $ns }}
  labels:
    {{- include "factory.labels" $ | nindent 4 }}
    app.kubernetes.io/component: operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "factory.operatorName" $ }}
subjects:
  - kind: ServiceAccount
    name: {{ include "factory.operatorName" $ }}
    namespace: {{ $.Release.Namespace }}
{{- end }}
{{- $taskNamespaces := .Values.operator.workerPools.taskNamespaces }}
{{- if not $taskNamespaces }}
{{- $taskNamespaces = list "factory-runs" }}
{{- end }}
{{- range $ns := $taskNamespaces }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ printf "%s-workerpool" (include "factory.operatorName" $) | trunc 63 | trimSuffix "-" }}
  namespace: {{ $ns }}
  labels:
    {{- include "factory.labels" $ | nindent 4 }}
    app.kubernetes.io/component: operator
rules:
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["roles", "rolebindings"]
    verbs: ["create", "get", "list", "watch", "update", "patch", "delete"]
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
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ printf "%s-workerpool" (include "factory.operatorName" $) | trunc 63 | trimSuffix "-" }}
  namespace: {{ $ns }}
  labels:
    {{- include "factory.labels" $ | nindent 4 }}
    app.kubernetes.io/component: operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ printf "%s-workerpool" (include "factory.operatorName" $) | trunc 63 | trimSuffix "-" }}
subjects:
  - kind: ServiceAccount
    name: {{ include "factory.operatorName" $ }}
    namespace: {{ $.Release.Namespace }}
{{- end }}
{{- if and .Values.operator.admissionWebhook.enabled (not .Values.operator.admissionWebhook.certManager) }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "factory.operatorName" . }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
rules:
  - apiGroups: ["admissionregistration.k8s.io"]
    resources: ["validatingwebhookconfigurations"]
    resourceNames: [{{ include "factory.operatorValidatingName" . | quote }}]
    verbs: ["get", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "factory.operatorName" . }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "factory.operatorName" . }}
subjects:
  - kind: ServiceAccount
    name: {{ include "factory.operatorName" . }}
    namespace: {{ .Release.Namespace }}
{{- end }}
{{- if and .Values.operator.admissionWebhook.enabled .Values.operator.admissionWebhook.certManager }}
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: {{ include "factory.operatorName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ include "factory.operatorWebhookName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
spec:
  secretName: {{ include "factory.operatorWebhookName" . }}
  duration: 24h
  renewBefore: 1h
  issuerRef:
    name: {{ include "factory.operatorName" . }}
    kind: Issuer
  dnsNames:
    - {{ include "factory.operatorWebhookName" . }}
    - {{ include "factory.operatorWebhookName" . }}.{{ .Release.Namespace }}.svc
    - {{ include "factory.operatorWebhookName" . }}.{{ .Release.Namespace }}.svc.cluster.local
{{- end }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "factory.operatorWebhookName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
spec:
  type: ClusterIP
  selector:
    {{- include "factory.selector" (list "operator" .) | nindent 4 }}
  ports:
    - name: webhook
      port: 9443
      targetPort: webhook
      protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "factory.operatorName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
spec:
  replicas: {{ .Values.operator.replicas }}
  selector:
    matchLabels:
      {{- include "factory.selector" (list "operator" .) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "factory.labels" . | nindent 8 }}
        app.kubernetes.io/component: operator
    spec:
      serviceAccountName: {{ include "factory.operatorName" . }}
      automountServiceAccountToken: true
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
        - name: operator
          image: {{ include "factory.image" (list . .Values.operator.image.repository) | quote }}
          imagePullPolicy: {{ .Values.global.image.pullPolicy }}
          ports:
            - name: webhook
              containerPort: 9443
              protocol: TCP
            - name: health
              containerPort: 8081
              protocol: TCP
          env:
            - name: FACTORY_URL
              value: {{ printf "http://%s:8080" (include "factory.factorydName" .) | quote }}
            - name: FACTORY_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ include "factory.apiSecretName" . }}
                  key: {{ .Values.controlPlane.api.apiKeyKey }}
            - name: OPERATOR_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            - name: WATCH_NAMESPACES
              value: {{ include "factory.operatorNamespaces" . | quote }}
            - name: WEBHOOK_ENABLED
              value: {{ .Values.operator.admissionWebhook.enabled | quote }}
            - name: OPERATOR_CERT_MANAGER
              value: {{ .Values.operator.admissionWebhook.certManager | quote }}
            - name: WEBHOOK_SECRET_NAME
              value: {{ include "factory.operatorWebhookName" . | quote }}
            - name: WEBHOOK_CONFIG_NAME
              value: {{ include "factory.operatorValidatingName" . | quote }}
            - name: WEBHOOK_SERVICE_NAME
              value: {{ include "factory.operatorWebhookName" . | quote }}
            - name: WEBHOOK_CERT_DIR
              value: /tmp/k8s-webhook-server/serving-certs
            - name: WORKER_IMAGE
              value: {{ include "factory.image" (list . .Values.worker.image.repository) | quote }}
            - name: WORKER_IMAGE_PULL_POLICY
              value: {{ .Values.global.image.pullPolicy | quote }}
            - name: WORKER_WEBSOCKET_URL
              value: {{ printf "ws://%s.%s.svc:8080/api/v1/selfhosted/worker/ws" (include "factory.factorydName" .) .Release.Namespace | quote }}
            - name: WORKER_SERVER_ROOT_URL
              value: {{ printf "http://%s.%s.svc:8080" (include "factory.factorydName" .) .Release.Namespace | quote }}
            - name: WORKER_API_KEY_SECRET_NAME
              value: {{ .Values.operator.workerPools.apiKeySecret.name | default "factory-worker" | quote }}
            - name: WORKER_API_KEY_SECRET_KEY
              value: {{ .Values.operator.workerPools.apiKeySecret.key | default "FACTORY_API_KEY" | quote }}
            - name: WORKERPOOL_TASK_NAMESPACES
              value: {{ include "factory.operatorTaskNamespaces" . | quote }}
            - name: WORKER_FULLNAME
              value: {{ include "factory.fullname" . | quote }}
            - name: WORKER_APP_NAME
              value: {{ include "factory.name" . | quote }}
            - name: WORKER_RELEASE_NAME
              value: {{ .Release.Name | quote }}
            - name: WORKER_IMAGE_PULL_SECRETS
              value: {{ include "factory.operatorPullSecrets" . | quote }}
          readinessProbe:
            httpGet:
              path: /readyz
              port: health
            initialDelaySeconds: 2
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /healthz
              port: health
            initialDelaySeconds: 15
            periodSeconds: 10
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
          {{- with .Values.operator.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- if and .Values.operator.admissionWebhook.enabled .Values.operator.admissionWebhook.certManager }}
          volumeMounts:
            - name: webhook-cert
              mountPath: /tmp/k8s-webhook-server/serving-certs
              readOnly: true
          {{- end }}
      {{- if and .Values.operator.admissionWebhook.enabled .Values.operator.admissionWebhook.certManager }}
      volumes:
        - name: webhook-cert
          secret:
            secretName: {{ include "factory.operatorWebhookName" . }}
      {{- end }}
{{- if .Values.operator.admissionWebhook.enabled }}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: {{ include "factory.operatorValidatingName" . }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    app.kubernetes.io/component: operator
  {{- if .Values.operator.admissionWebhook.certManager }}
  annotations:
    cert-manager.io/inject-ca-from: {{ printf "%s/%s" .Release.Namespace (include "factory.operatorWebhookName" .) | quote }}
  {{- end }}
webhooks:
  {{- range $hook := list (dict "name" "factory" "resource" "factories" "path" "/validate-factory-dev-v1alpha1-factory") (dict "name" "factoryagent" "resource" "factoryagents" "path" "/validate-factory-dev-v1alpha1-factoryagent") (dict "name" "automation" "resource" "automations" "path" "/validate-factory-dev-v1alpha1-automation") (dict "name" "runner" "resource" "runners" "path" "/validate-factory-dev-v1alpha1-runner") (dict "name" "scorer" "resource" "scorers" "path" "/validate-factory-dev-v1alpha1-scorer") }}
  - name: {{ printf "%s.%s.factory.dev" $hook.name (include "factory.operatorName" $) | quote }}
    failurePolicy: Fail
    sideEffects: None
    admissionReviewVersions: ["v1"]
    timeoutSeconds: 10
    namespaceSelector:
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: In
          values:
            {{- range split "," (include "factory.operatorNamespaces" $) }}
            - {{ . | quote }}
            {{- end }}
    clientConfig:
      service:
        name: {{ include "factory.operatorWebhookName" $ }}
        namespace: {{ $.Release.Namespace }}
        path: {{ $hook.path }}
        port: 9443
      {{- if not $.Values.operator.admissionWebhook.certManager }}
      caBundle: eA==
      {{- end }}
    rules:
      - apiGroups: ["factory.dev"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE", "UPDATE"]
        resources: [{{ $hook.resource | quote }}]
        scope: Namespaced
  {{- end }}
{{- end }}
{{- end }}
{{- end -}}
