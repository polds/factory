#!/usr/bin/env bash
# Pinned helm, kind, kubectl, and kubeconform for the chart scripts.
# Source this file. It does not download anything until an install_* function runs.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  echo "source scripts/ci/helm-lib.sh" >&2
  exit 2
fi

# Consumed by the scripts that source this file.
export HELM_VERSION=3.22.1
export HELM_SHA256=896cf63684a42a723d61c980369d6e588330927deb13676ca0d85006dd6da0ea
export KIND_VERSION=0.33.0
export KIND_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
export KUBECTL_VERSION=1.34.11
export KUBECTL_SHA256=8efbb9435132a190920eb65a47a8c1ecf755ad85ab57a600c9bedbab460bb7a8
export KUBECONFORM_VERSION=0.8.0
export KUBECONFORM_SHA256=9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883
export KUBERNETES_SCHEMA_VERSION=1.34.11
export KIND_NODE_IMAGE=kindest/node:v1.34.11@sha256:44e222ee2132dab25ff87301682f89eb82c7880ea3a1bf543bfe9708fd08d67d

export POSTGRES_IMAGE=postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab
export BUSYBOX_IMAGE=busybox:1.36@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
export MINIO_IMAGE=bitnamilegacy/minio:2025.7.23-debian-12-r5@sha256:6dabb4a2088c9a79908de3bc05f4586c23ad2182c8908e7e3acbf61c1467fb20
export MINIO_CLIENT_IMAGE=bitnamilegacy/minio-client:2025.7.21-debian-12-r3@sha256:73bd39f7899a0cef12b8dd5df13aa93a3ed1aaa44236542442e9ac76819ac158

export KUBECONFORM_SCHEMA_LOCATION='https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/{{ .NormalizedKubernetesVersion }}-standalone{{ .StrictSuffix }}/{{ .ResourceKind }}{{ .KindSuffix }}.json'

helm_cache_dir() {
  printf '%s\n' "${FACTORY_HELM_TOOLS:-${TMPDIR:-/tmp}/factory-helm-tools}"
}

verify_sha256() {
  local file="$1"
  local want="$2"
  local got
  got="$(sha256sum "${file}" | awk '{print $1}')"
  if [[ "${got}" != "${want}" ]]; then
    echo "checksum mismatch for ${file}" >&2
    echo "got  ${got}" >&2
    echo "want ${want}" >&2
    exit 1
  fi
}

install_verified_bin() {
  local url="$1"
  local sha="$2"
  local dest="$3"
  if [[ -x "${dest}" ]]; then
    local got
    got="$(sha256sum "${dest}" | awk '{print $1}')"
    if [[ "${got}" == "${sha}" ]]; then
      return 0
    fi
  fi
  local tmp
  tmp="$(mktemp)"
  curl -fsSL -o "${tmp}" "${url}"
  verify_sha256 "${tmp}" "${sha}"
  mkdir -p "$(dirname "${dest}")"
  install -m 0755 "${tmp}" "${dest}"
  rm -f "${tmp}"
}

install_helm() {
  local cache dest
  cache="$(helm_cache_dir)"
  dest="${cache}/helm-${HELM_VERSION}"
  if [[ ! -x "${dest}" ]]; then
    local tmp archive
    tmp="$(mktemp -d)"
    archive="${tmp}/helm.tgz"
    curl -fsSL -o "${archive}" "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz"
    verify_sha256 "${archive}" "${HELM_SHA256}"
    tar -C "${tmp}" -xzf "${archive}"
    mkdir -p "${cache}"
    install -m 0755 "${tmp}/linux-amd64/helm" "${dest}"
    rm -rf "${tmp}"
  fi
  HELM="${dest}"
  export HELM
}

install_kind() {
  local dest
  dest="$(helm_cache_dir)/kind-${KIND_VERSION}"
  install_verified_bin \
    "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-amd64" \
    "${KIND_SHA256}" \
    "${dest}"
  KIND="${dest}"
  export KIND
}

install_kubectl() {
  local dest
  dest="$(helm_cache_dir)/kubectl-${KUBECTL_VERSION}"
  install_verified_bin \
    "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
    "${KUBECTL_SHA256}" \
    "${dest}"
  KUBECTL="${dest}"
  export KUBECTL
}

install_kubeconform() {
  local cache dest
  cache="$(helm_cache_dir)"
  dest="${cache}/kubeconform-${KUBECONFORM_VERSION}"
  if [[ ! -x "${dest}" ]]; then
    local tmp archive
    tmp="$(mktemp -d)"
    archive="${tmp}/kubeconform.tgz"
    curl -fsSL -o "${archive}" \
      "https://github.com/yannh/kubeconform/releases/download/v${KUBECONFORM_VERSION}/kubeconform-linux-amd64.tar.gz"
    verify_sha256 "${archive}" "${KUBECONFORM_SHA256}"
    tar -C "${tmp}" -xzf "${archive}"
    mkdir -p "${cache}"
    install -m 0755 "${tmp}/kubeconform" "${dest}"
    rm -rf "${tmp}"
  fi
  KUBECONFORM="${dest}"
  export KUBECONFORM
}
