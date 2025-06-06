#!/usr/bin/env bash

set -euo pipefail

readonly API_PKGS=(
  "github.com/akuity/kargo/api/v1alpha1"
  "github.com/akuity/kargo/api/stubs/rollouts/v1alpha1"
  "github.com/akuity/kargo/api/rbac/v1alpha1"
)

readonly APIMACHINERY_PKGS=(
  "-k8s.io/api/core/v1"
  "-k8s.io/api/batch/v1"
  "-k8s.io/api/rbac/v1"
  "-k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
  "-k8s.io/apimachinery/pkg/util/intstr"
  "-k8s.io/apimachinery/pkg/api/resource"
  "-k8s.io/apimachinery/pkg/runtime/schema"
  "-k8s.io/apimachinery/pkg/runtime"
  "-k8s.io/apimachinery/pkg/apis/meta/v1"
)

# Default to cleaning up the temporary directory
CLEANUP_TMP="true"

# Function to display usage
function usage() {
  echo "Usage: $0 [options]"
  echo "Options:"
  echo "  --no-cleanup    Don't clean up temporary directory after execution"
  echo "  -h, --help      Display this help message"
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-cleanup)
      CLEANUP_TMP="false"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      usage
      exit 1
      ;;
  esac
done

function main() {
  set -x

  # go-to-protobuf is used for generating .proto and .pb.go files from
  # Kubebuilder structs.
  #
  # It has an inconvenient requirement that the directory structure of the
  # project must directly mirror the import paths of the packages.
  #
  # To account for this, we'll do all go-to-protobuf-related work in a temporary
  # directory, and copy the generated .proto files back to the project root when
  # we're done.

  { msg "Changing working directory to temporary directory..."; } 2> /dev/null
  cd "${tmp_dir}"

  { msg "Initializing dummy module and workspace..."; } 2> /dev/null
  go mod init github.com/akuity
  go work init

  set +x
  msg "Copying source to temporary directory...";
  local build_src_dir="${tmp_dir}/src/github.com/akuity/kargo"
  mkdir -p "${build_src_dir}"
  find "$proj_dir" \( \
    -name '*.go' -o \
    -name 'go.mod' -o \
    -name 'go.sum' \
  \) -type f | while read -r file; do
    rel_path="${file#$proj_dir/}"
    dest_file="$build_src_dir/$rel_path"
    dest_dir=$(dirname "$dest_file")
    mkdir -p "$dest_dir"
    cp "$file" "$dest_file"
  done

  msg "Vendoring dependencies for .proto files generation..."
  set -x
  # IMPORTANT: This doesn't work unless all our modules (kargo, kargo/api, and
  # kargo/pkg) are vendored into the workspace. 
  go work use ./src/github.com/akuity/kargo
  go work use ./src/github.com/akuity/kargo/api
  go work use ./src/github.com/akuity/kargo/pkg
  go work sync
  go work vendor

  { msg "Generating .proto and .pb.go files from Kubebuilder structs..."; } 2> /dev/null
  go-to-protobuf \
    --go-header-file="${proj_dir}/hack/boilerplate.go.txt" \
    --packages="$(IFS=, ; echo "${API_PKGS[*]}")" \
    --apimachinery-packages="$(IFS=, ; echo "${APIMACHINERY_PKGS[*]}")" \
    --proto-import="${proj_dir}/hack/include" \
    --proto-import="${tmp_dir}/vendor" \
    --output-dir="${tmp_dir}/src"

  { msg "Copying generated .proto and .pb.go files back to the project root..."; } 2> /dev/null
  find "$build_src_dir/api" \( \
    -name '*.proto' -o \
    -name '*.pb.go' \
  \) -type f | while read -r file; do
    rel_path="${file#"$build_src_dir"/}"
    dest_file="$proj_dir/$rel_path"
    dest_dir=$(dirname "$dest_file")
    mkdir -p "$dest_dir"
    cp "$file" "$dest_file"
  done

  # At this point, .proto and .pb.go files generated from Kubebuilder structs
  # are all where they belong.

  { msg "Returning to the project root..."; } 2> /dev/null
  cd "${proj_dir}"

  # Next, we'll use buf to generate Go and TypeScript bindings from the .proto
  # files.
  #
  # The easiest way to get buf to see third-party .proto files is to
  # (temporarily) vendor them into the project and add vendor/ as a path in the
  # buf.yaml file.

  { msg "Vendoring dependencies (temporarily) for code generation..."; } 2> /dev/null
  go mod vendor

  { msg "Injecting protobuf struct tags into Kubebuilder structs..."; } 2> /dev/null
  # buf.kubebuilder.gen.yaml sends output to a temporary location because we
  # don't actually want to keep the generated files. We're only using them as an
  # intermediate step to inject struct tags.
  #
  # NOTE: We do this only for the APIs we define ourselves. It's not applied to
  # the "knockoff" Argo CD and Argo Rollouts types.
  buf generate . --path api/v1alpha1 \
    --template=buf.kubebuilder.gen.yaml
  buf generate . --path api/rbac \
    --template=buf.kubebuilder.gen.yaml
  go run ./hack/codegen/prototag/main.go \
    -src-dir="${proj_dir}/tmp/api/v1alpha1" \
    -dst-dir="${proj_dir}/api/v1alpha1"
  go run ./hack/codegen/prototag/main.go \
    -src-dir="${proj_dir}/tmp/api/rbac/v1alpha1" \
    -dst-dir="${proj_dir}/api/rbac/v1alpha1"

  { msg "Generating .pb.go and .connect.go files from service.proto"; } 2> /dev/null
  buf generate . --path api/service

  { msg "Generating TypeScript bindings for UI..."; } 2> /dev/null
  buf generate . --path api \
    --include-imports \
    --template=buf.ui.gen.yaml
}

function msg() {
  echo -e "\033[1;32m$1\033[0m"
}

function clean() {
  { msg "Cleaning up all intermediate resources..."; } 2> /dev/null
  rm -rf "${proj_dir}/vendor" || true
  rm -rf "${proj_dir}/tmp" || true

  if [[ "${CLEANUP_TMP}" == "true" ]]; then
    { msg "Cleaning up temporary directory..."; } 2> /dev/null
    rm -rf "${tmp_dir}" || true
  else
    { msg "Temporary directory '${tmp_dir}' preserved"; } 2> /dev/null
  fi

  { msg "Done"; } 2> /dev/null
}

msg "Finding project root..."
proj_dir=$(dirname "${0}")
proj_dir=$(readlink -f "${proj_dir}/../..")
msg "Project root is ${proj_dir}"

# Include local binaries in the PATH
export PATH="${proj_dir}/hack/bin:${PATH}"

msg "Creating temporary directory..."
tmp_dir=$(mktemp -d)
msg "Temporary directory is ${tmp_dir}"

trap 'clean' EXIT

main
