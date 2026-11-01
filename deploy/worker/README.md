# Worker image

The image is the unmodified upstream `oz-agent-worker`. `WORKER_REF` in the Dockerfile is the pin.

## Kubernetes backend

`kubernetes.example.yaml` is a worker config for the Kubernetes backend. The keys match the backend block in docs/06. Pull secrets and environment credentials are names (`imagePullSecrets`, `secretKeyRef`). The file has no secret values, and the image build does not copy it.
