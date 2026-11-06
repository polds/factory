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
{{- end }}
{{- if .Values.scanSecurity.egress.networkPolicy }}
{{- range .Values.workers }}
{{- if .enabled }}
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
  podSelector:
    matchLabels:
      factory.dev/pool: {{ .pool | quote }}
  policyTypes:
    - Egress
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
      ports:
        - protocol: UDP
          port: 53
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except:
              - 169.254.169.254/32
      ports:
        - protocol: TCP
          port: 443
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
