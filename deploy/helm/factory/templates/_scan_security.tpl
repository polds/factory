{{- define "factory.scanSecurity" -}}
{{- if .Values.scanSecurity.enabled }}
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
