#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# Configuration
VERSION="${VERSION:-$(git -C "${SCRIPT_DIR}" rev-parse --short HEAD)}"
BINARY_NAME="bazel-remote"

OCIR_REGISTRY="${OCIR_REGISTRY:-fra.ocir.io}"
OCIR_NAMESPACE="${OCIR_NAMESPACE:-}"  # Set via OCI CLI if not provided
REPO_NAME="bazel-remote/${BINARY_NAME}"
IMAGE_TAG="${IMAGE_TAG:-${VERSION}}"

_oci_compartment_id() {
    oci iam compartment list --name dev | jq -r '.data[]."compartment-id"'
}

_ocir_repo_ns() {
    if [[ -n "${OCIR_NAMESPACE}" ]]; then
        echo "${OCIR_NAMESPACE}"
        return 0
    fi
    oci os ns get | jq -r '.data'
}

_ocir_repo_exists() {
    local n
    n=$(oci artifacts container repository list \
        --compartment-id "$(_oci_compartment_id)" \
        --display-name "${1}" | jq '.data.items | length')
    [[ ${n} -gt 0 ]]
}

_ocir_repo_create() {
    echo "INFO Creating OCIR repo ${1}..."
    oci artifacts container repository create \
        --display-name "${1}" \
        --compartment-id "$(_oci_compartment_id)"
}

build_image() {
    local ns
    ns=$(_ocir_repo_ns)
    local full_image="${OCIR_REGISTRY}/${ns}/${REPO_NAME}:${IMAGE_TAG}"
    echo "INFO Building Docker image ${full_image}..."
    docker build --platform linux/amd64 \
        --build-arg VERSION="${VERSION}" \
        -t "${full_image}" "${SCRIPT_DIR}"
    echo "INFO Build complete"
}

push_image() {
    local ns
    ns=$(_ocir_repo_ns)
    local full_image="${OCIR_REGISTRY}/${ns}/${REPO_NAME}:${IMAGE_TAG}"

    # Create repo if it doesn't exist
    if ! _ocir_repo_exists "${REPO_NAME}"; then
        _ocir_repo_create "${REPO_NAME}"
    fi

    echo "INFO Pushing ${full_image}..."
    docker push "${full_image}"
    echo "INFO Push complete"
}

build_and_push() {
    build_image
    push_image
}

usage() {
    cat <<EOF
Build and push custom bazel-remote Docker image to OCIR

This is a forked version of bazel-remote with shared storage mode support:
- --shared_storage_mode: Enable cross-replica file discovery on shared filesystem
- --shared_storage_leader: Designate this instance as the GC leader
- --shared_storage_gc_interval: GC interval for the leader (default: 5m)

Usage: $(basename "$0") [--tag TAG] <command>

Options:
    --tag TAG        Docker image tag (overrides IMAGE_TAG env var)

Commands:
    build_and_push   Build image and push to OCIR
    build_image      Build Docker image
    push_image       Push image to OCIR

Environment variables:
    VERSION          Git commit or version tag (default: current git short hash)
    IMAGE_TAG        Docker image tag (default: same as VERSION)
    OCIR_REGISTRY    OCIR registry URL (default: ${OCIR_REGISTRY})
    OCIR_NAMESPACE   OCIR namespace (default: auto-detected via OCI CLI)

Example:
    $(basename "$0") build_and_push
    VERSION=v1.0.0 $(basename "$0") build_and_push
EOF
}

main() {
    if [[ $# -eq 0 ]]; then
        usage
        exit 1
    fi

    # Check for --tag argument
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --tag)
                IMAGE_TAG="$2"
                shift 2
                ;;
            *)
                break
                ;;
        esac
    done

    "$@"
}

main "$@"
