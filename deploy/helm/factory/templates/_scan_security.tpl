{{- define "factory.scanSecurity" -}}
{{- if .Values.scanSecurity.enabled }}
{{- if .Values.scanSecurity.egress.requireCNI }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "factory.fullname" . }}-scan-cni
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "factory.labels" . | nindent 4 }}
    factory.dev/component: scan-security
  annotations:
    factory.dev/require-cni: {{ .Values.scanSecurity.egress.cniProvider | default "enforcing" | quote }}
data:
  README: |
    scanSecurity.egress.requireCNI opts task namespaces into CNI-enforced egress.
    kindnet default routes do not satisfy G14; M10-G must prove the selected CNI
    denies metadata, direct-IP bypass, and cross-namespace egress on the actual cluster.
    This render is not that proof. paidProbeAllowed stays false.
{{- end }}
{{- if .Values.scanSecurity.egress.networkPolicy }}
{{- if not .Values.scanSecurity.egress.routes }}
{{- fail "scanSecurity.egress.networkPolicy requires scanSecurity.egress.routes" }}
{{- end }}
{{- range .Values.workers }}
{{- if .enabled }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "factory.workerFullname" (list $ .) }}-egress-routes
  namespace: {{ .kubernetesBackend.namespace }}
  labels:
    {{- include "factory.labels" $ | nindent 4 }}
    factory.dev/component: egress-proxy
data:
  deny-metadata: 169.254.169.254/32
  routes: |
    {{- range $.Values.scanSecurity.egress.routes }}
    {{ .name }}={{ required "route host" .host }}:{{ .port }}
    {{- end }}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "factory.workerFullname" (list $ .) }}-scan-egress
  namespace: {{ .kubernetesBackend.namespace }}
  labels:
    {{- include "factory.labels" $ | nindent 4 }}
    factory.dev/component: scan-security
  {{- if $.Values.scanSecurity.egress.requireCNI }}
  annotations:
    factory.dev/require-cni: {{ $.Values.scanSecurity.egress.cniProvider | default "enforcing" | quote }}
  {{- end }}
spec:
  # Task pods live in kubernetesBackend.namespace. The worker stamps
  # factory.dev/scan-task via extra_labels. Worker Deployments in the
  # release namespace carry factory.dev/pool and are not selected here.
  podSelector:
    matchLabels:
      factory.dev/scan-task: "true"
  policyTypes:
    - Egress
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: {{ $.Release.Namespace }}
          podSelector:
            matchLabels:
              factory.dev/component: egress-proxy
      ports:
        - protocol: TCP
          port: {{ $.Values.scanSecurity.egress.proxyPort }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
