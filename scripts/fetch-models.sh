#!/usr/bin/env bash

set -euo pipefail

MANIFEST="models/manifest.json"
MODELS_DIR_ARG=""
FORCE=0
ORT_ONLY=0
ORT_VERSION="1.29.0"
ORT_SHA256=""
LIB_DIR="lib"

PARTIAL=""
ORT_TMP=""
ORT_STAGE=""

usage() {
  cat <<'EOF'
Usage: scripts/fetch-models.sh [OPTIONS]

Download the ONNX models declared in a manifest, verify their SHA-256
digests, and install them for the engine. Model URLs must be absolute
https:// URLs. Models whose source type is "local" are never downloaded:
the script only reports whether the file is present. Re-running the script
is cheap: files already present with a matching digest are skipped.

Options:
  --manifest PATH    manifest file to read (default: models/manifest.json)
  --dir DIR          directory to install models into
                     (default: $MODELS_DIR when set, otherwise ./assets/models)
  --force            download even when the file is already present
  --ort-only         download ONNX Runtime into ./lib and skip the models
  --ort-version VER  ONNX Runtime version to download (default: 1.29.0)
  --ort-sha256 HEX   SHA-256 of the ONNX Runtime archive; overrides the
                     pinned digest and is required for unpinned versions
  --help             print this help and exit

Environment:
  MODELS_DIR         default value for --dir
  ORT_LIB            path to an ONNX Runtime shared library. When set, the
                     engine loads that library instead of lib/libonnxruntime.*;
                     this script never writes ORT_LIB. Use --ort-only to
                     install the bundled library into ./lib.
  FETCH_MODELS_ALLOW_LOCAL_URLS
                     test-only escape hatch. When set to 1, file:// and
                     http:// model URLs are accepted as well. The default
                     (unset) accepts only absolute https:// URLs.

--ort-only downloads the official ONNX Runtime release for the host OS and
architecture: macOS arm64 and Linux x86_64/aarch64 at any released version,
macOS x86_64 (Intel) only up to 1.23.2 (use --ort-version 1.23.2). It
extracts the libonnxruntime shared libraries and symlinks into ./lib,
replacing existing files atomically, and verifies that libonnxruntime.dylib
(macOS) or libonnxruntime.so (Linux) exists. The downloaded archive is
checked against a pinned SHA-256 digest (1.29.0 on every platform, plus
1.23.2 for macOS Intel) before extraction; other versions require
--ort-sha256. Re-run with --force to replace an existing install.

Exit status: 0 on success, 1 on download or verification failure, 2 on
usage errors.
EOF
}

info() {
  printf 'fetch-models: %s\n' "$*"
}

fail() {
  printf 'fetch-models: error: %s\n' "$*" >&2
  exit 1
}

report_mismatch() {
  printf 'fetch-models: error: %s: sha256 mismatch\n' "$1" >&2
  printf '  expected: %s\n  actual:   %s\n' "$2" "$3" >&2
}

require_tools() {
  local tool
  for tool in "$@"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      fail "$tool is required but was not found in PATH"
    fi
  done
}

cleanup() {
  if [ -n "$PARTIAL" ]; then
    rm -f -- "$PARTIAL"
  fi
  if [ -n "$ORT_TMP" ]; then
    rm -rf -- "$ORT_TMP"
  fi
  if [ -n "$ORT_STAGE" ]; then
    rm -rf -- "$ORT_STAGE"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

curl_fetch() {
  local url="$1" out="$2" proto verbosity
  proto=(--proto '=https' --proto-redir '=https')
  if [ "${FETCH_MODELS_ALLOW_LOCAL_URLS:-}" = "1" ]; then
    proto=(--proto '=https,http,file' --proto-redir '=https,http,file')
  fi
  verbosity=(-sS)
  if [ -t 2 ]; then
    verbosity=(--progress-bar)
  fi
  curl --fail --location --retry 3 --retry-connrefused \
    --connect-timeout 30 --max-time 600 --globoff \
    "${proto[@]}" "${verbosity[@]}" -o "$out" -- "$url"
}

HASH=""
hash_file() {
  local out
  if command -v sha256sum >/dev/null 2>&1; then
    out="$(sha256sum -- "$1")" || fail "failed to hash $1"
  elif command -v shasum >/dev/null 2>&1; then
    out="$(shasum -a 256 -- "$1")" || fail "failed to hash $1"
  else
    fail "no SHA-256 tool found: install sha256sum (coreutils) or shasum"
  fi
  HASH="$(printf '%s' "${out%% *}" | tr 'A-F' 'a-f')"
}

validate_sha256() {
  case "$1" in
    "" | *[!0-9a-fA-F]*) fail "$2: manifest sha256 is not hexadecimal: \"$1\"" ;;
  esac
  if [ "${#1}" -ne 64 ]; then
    fail "$2: manifest sha256 must be 64 hex characters, got ${#1}"
  fi
}

check_hash() {
  local file="$1" want="$2" label="$3" want_lc
  validate_sha256 "$want" "$label"
  want_lc="$(printf '%s' "$want" | tr 'A-F' 'a-f')"
  hash_file "$file"
  [ "$HASH" = "$want_lc" ]
}

pinned_ort_sha256() {
  case "$1" in
    onnxruntime-osx-arm64-1.29.0.tgz)
      printf '%s\n' "d0706fc34f315d8c88639d0a8c81f2e09e815f282cabed3493c06a054352cf92"
      ;;
    onnxruntime-osx-x86_64-1.23.2.tgz)
      printf '%s\n' "d10359e16347b57d9959f7e80a225a5b4a66ed7d7e007274a15cae86836485a6"
      ;;
    onnxruntime-linux-x64-1.29.0.tgz)
      printf '%s\n' "c3fddc4f139a045b0c4902c57410f0694f1c2fdf9b6939fbe38b1aeae7cd14ba"
      ;;
    onnxruntime-linux-aarch64-1.29.0.tgz)
      printf '%s\n' "e1799098ebc054b370f6176a450f158720f297818c613e5dc99b92e2ec82346f"
      ;;
    *)
      return 1
      ;;
  esac
}

install_mode() {
  local mask
  mask="$(umask)"
  printf '%03o' "$(( 0666 & ~8#$mask ))"
}

fetch_models() {
  local records id file sha type url dest tmp
  require_tools curl python3

  if [ ! -f "$MANIFEST" ]; then
    fail "manifest not found: $MANIFEST (pass --manifest PATH)"
  fi

  if ! records="$(python3 - "$MANIFEST" <<'PY'
import json
import os
import sys
from urllib.parse import urlsplit

path = sys.argv[1]
allow_local = os.environ.get("FETCH_MODELS_ALLOW_LOCAL_URLS") == "1"


def die(message):
    sys.exit(f"{path}: {message}")


try:
    with open(path, encoding="utf-8-sig") as fh:
        manifest = json.load(fh)
except (OSError, ValueError) as err:
    die(str(err))

models = manifest.get("models")
if not isinstance(models, list):
    die("missing models list")

rows = []
seen = set()
for model in models:
    if not isinstance(model, dict):
        die("model entry is not an object")
    model_id = model.get("id")
    if not isinstance(model_id, str) or not model_id:
        die("model entry with empty or non-string id")
    if any(ch in model_id for ch in "\t\r\n"):
        die(f"model {model_id!r}: id must not contain tabs or newlines")
    if model_id in seen:
        die(f"duplicate model id {model_id!r}")
    seen.add(model_id)

    source = model.get("source")
    if not isinstance(source, dict):
        source = {}
    source_type = source.get("type")
    if source_type not in ("hf", "release", "local"):
        die(f"model {model_id!r}: invalid source type {source_type!r}")

    file_name = model.get("file")
    if (not isinstance(file_name, str) or
            file_name in ("", ".", "..") or
            "/" in file_name or
            any(ch in file_name for ch in "\t\r\n") or
            not file_name.lower().endswith(".onnx")):
        die(f"model {model_id!r}: invalid file name {file_name!r} "
            "(must be a bare *.onnx file name)")

    sha = model.get("sha256")
    if (not isinstance(sha, str) or len(sha) != 64 or
            any(ch not in "0123456789abcdefABCDEF" for ch in sha)):
        die(f"model {model_id!r}: invalid sha256 {sha!r} "
            "(must be 64 hex characters)")

    url = source.get("url") or ""
    if not isinstance(url, str) or any(ch in url for ch in "\t\r\n"):
        die(f"model {model_id!r}: invalid source url")
    if source_type in ("hf", "release"):
        if not url:
            die(f"model {model_id!r}: source type {source_type!r} requires a url")
        if url.startswith("-"):
            die(f"model {model_id!r}: url must not start with '-': {url!r}")
        parts = urlsplit(url)
        if parts.scheme == "https" and parts.netloc:
            pass
        elif allow_local and parts.scheme in ("file", "http"):
            pass
        else:
            hint = ""
            if not allow_local:
                hint = (" (set FETCH_MODELS_ALLOW_LOCAL_URLS=1 to allow "
                        "file:// and http:// for tests)")
            die(f"model {model_id!r}: url must be an absolute https:// url{hint}")

    rows.append((model_id, file_name, sha, source_type, url))

for row in rows:
    print("\t".join(row))
PY
)"; then
    fail "could not parse manifest: $MANIFEST"
  fi

  if ! mkdir -p -- "$DIR"; then
    fail "could not create model directory: $DIR"
  fi

  while IFS=$'\t' read -r id file sha type url; do
    [ -n "$id" ] || continue
    dest="$DIR/$file"
    case "$type" in
      hf | release)
        if [ -e "$dest" ] && [ "$FORCE" -eq 0 ]; then
          if check_hash "$dest" "$sha" "model $id ($file)"; then
            info "model $id: up to date, skipping $file"
            continue
          fi
          report_mismatch "model $id ($dest)" "$sha" "$HASH"
          fail "delete the file or re-run with --force to re-download it"
        fi
        if ! tmp="$(mktemp -- "$dest.partial.XXXXXX")"; then
          fail "model $id: could not create a temporary file next to $dest"
        fi
        PARTIAL="$tmp"
        info "model $id: downloading $url"
        if ! curl_fetch "$url" "$tmp"; then
          fail "model $id: download failed: $url"
        fi
        if ! check_hash "$tmp" "$sha" "model $id ($file)"; then
          report_mismatch "model $id ($url)" "$sha" "$HASH"
          rm -f -- "$tmp"
          PARTIAL=""
          exit 1
        fi
        chmod -- "$(install_mode)" "$tmp" ||
          fail "model $id: could not set permissions on $tmp"
        mv -f -- "$tmp" "$dest"
        PARTIAL=""
        info "model $id: installed $file"
        ;;
      local)
        if [ -e "$dest" ]; then
          info "model $id: local file present, skipping $file"
        else
          info "model $id: local file missing, skipping $dest (user-provided)"
        fi
        ;;
      *)
        fail "model $id: unsupported source type \"$type\""
        ;;
    esac
  done <<<"$records"
}

fetch_ort() {
  local os arch asset libname sha target url tmp src stage entry
  require_tools curl tar

  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os/$arch" in
    Darwin/arm64 | Darwin/aarch64)
      asset="onnxruntime-osx-arm64-${ORT_VERSION}.tgz"
      libname="libonnxruntime.dylib"
      ;;
    Darwin/x86_64)
      asset="onnxruntime-osx-x86_64-${ORT_VERSION}.tgz"
      libname="libonnxruntime.dylib"
      ;;
    Linux/x86_64)
      asset="onnxruntime-linux-x64-${ORT_VERSION}.tgz"
      libname="libonnxruntime.so"
      ;;
    Linux/aarch64 | Linux/arm64)
      asset="onnxruntime-linux-aarch64-${ORT_VERSION}.tgz"
      libname="libonnxruntime.so"
      ;;
    *)
      fail "unsupported platform $os/$arch (supported: macOS arm64, Linux x86_64/aarch64; macOS x86_64 only up to ONNX Runtime 1.23.2)"
      ;;
  esac

  target="$LIB_DIR/$libname"
  if [ -f "$target" ] || [ -L "$target" ]; then
    if [ "$FORCE" -eq 0 ]; then
      info "onnxruntime: found $target, skipping (use --force to re-download)"
      return 0
    fi
  fi
  if [ "$os/$arch" = "Darwin/x86_64" ] && [ "$ORT_VERSION" != "1.23.2" ]; then
    fail "ONNX Runtime ${ORT_VERSION} has no macOS x86_64 (Intel) build: Intel support ended after 1.23.2; re-run with --ort-version 1.23.2 or set ORT_LIB to an existing library"
  fi

  sha="$ORT_SHA256"
  if [ -z "$sha" ] && ! sha="$(pinned_ort_sha256 "$asset")"; then
    fail "onnxruntime ${ORT_VERSION}: no pinned sha256 for $asset; pass --ort-sha256 HEX"
  fi
  validate_sha256 "$sha" "onnxruntime $asset"

  if ! mkdir -p -- "$LIB_DIR"; then
    fail "could not create library directory: $LIB_DIR"
  fi
  if ! ORT_TMP="$(mktemp -d -- "${TMPDIR:-/tmp}/fetch-models.XXXXXX")"; then
    fail "onnxruntime: could not create a temporary directory"
  fi
  tmp="$ORT_TMP"
  url="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${asset}"
  info "onnxruntime ${ORT_VERSION}: downloading $asset"
  if ! curl_fetch "$url" "$tmp/$asset"; then
    fail "onnxruntime: download failed: $url"
  fi
  if ! check_hash "$tmp/$asset" "$sha" "onnxruntime ${ORT_VERSION} ($asset)"; then
    report_mismatch "onnxruntime ${ORT_VERSION} ($url)" "$sha" "$HASH"
    exit 1
  fi
  info "onnxruntime ${ORT_VERSION}: sha256 verified ($asset)"
  info "onnxruntime ${ORT_VERSION}: extracting $asset"
  if ! tar -C "$tmp" -xzf "$tmp/$asset"; then
    fail "onnxruntime: could not extract $asset"
  fi
  src="$tmp/${asset%.tgz}/lib"
  if [ ! -d "$src" ]; then
    fail "onnxruntime: unexpected archive layout, no lib directory at $src"
  fi

  # Stage inside $LIB_DIR so each install is a same-filesystem rename, never a delete-then-copy.
  if ! ORT_STAGE="$(mktemp -d -- "$LIB_DIR/.ort-stage.XXXXXX")"; then
    fail "onnxruntime: could not create a staging directory in $LIB_DIR"
  fi
  stage="$ORT_STAGE"
  for entry in "$src"/libonnxruntime*; do
    if [ ! -f "$entry" ] && [ ! -L "$entry" ]; then
      continue
    fi
    cp -P -- "$entry" "$stage"/ ||
      fail "onnxruntime: could not stage $entry"
  done
  if [ ! -e "$stage/$libname" ]; then
    fail "onnxruntime: archive did not contain $libname"
  fi
  chmod 755 "$stage"/libonnxruntime* ||
    fail "onnxruntime: could not mark the library executable"
  for entry in "$stage"/libonnxruntime*; do
    if [ ! -f "$entry" ] && [ ! -L "$entry" ]; then
      continue
    fi
    mv -f -- "$entry" "$LIB_DIR"/ ||
      fail "onnxruntime: could not install $entry"
  done
  if [ ! -f "$target" ] && [ ! -L "$target" ]; then
    fail "onnxruntime: $target is missing after install"
  fi
  rm -rf -- "$tmp"
  ORT_TMP=""
  rm -rf -- "$stage"
  ORT_STAGE=""
  info "onnxruntime ${ORT_VERSION}: installed $target"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --manifest)
      if [ $# -lt 2 ] || [ -z "$2" ]; then
        printf 'fetch-models: error: --manifest requires a value\n' >&2
        exit 2
      fi
      MANIFEST="$2"
      shift 2
      ;;
    --dir)
      if [ $# -lt 2 ] || [ -z "$2" ]; then
        printf 'fetch-models: error: --dir requires a value\n' >&2
        exit 2
      fi
      MODELS_DIR_ARG="$2"
      shift 2
      ;;
    --ort-version)
      if [ $# -lt 2 ] || [ -z "$2" ]; then
        printf 'fetch-models: error: --ort-version requires a value\n' >&2
        exit 2
      fi
      ORT_VERSION="$2"
      shift 2
      ;;
    --ort-sha256)
      if [ $# -lt 2 ] || [ -z "$2" ]; then
        printf 'fetch-models: error: --ort-sha256 requires a value\n' >&2
        exit 2
      fi
      ORT_SHA256="$2"
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    --ort-only)
      ORT_ONLY=1
      shift
      ;;
    --help | -h)
      usage
      exit 0
      ;;
    -*)
      printf 'fetch-models: error: unknown option: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
    *)
      printf 'fetch-models: error: unexpected argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$ORT_VERSION" in
  "" | *[!0-9.]* | [!0-9]*)
    printf 'fetch-models: error: invalid --ort-version: %s (expected digits and dots, e.g. 1.29.0)\n' "$ORT_VERSION" >&2
    exit 2
    ;;
esac

if [ -n "$ORT_SHA256" ]; then
  case "$ORT_SHA256" in
    *[!0-9a-fA-F]*)
      printf 'fetch-models: error: invalid --ort-sha256: %s (expected 64 hex characters)\n' "$ORT_SHA256" >&2
      exit 2
      ;;
  esac
  if [ "${#ORT_SHA256}" -ne 64 ]; then
    printf 'fetch-models: error: invalid --ort-sha256: %s (expected 64 hex characters, got %d)\n' "$ORT_SHA256" "${#ORT_SHA256}" >&2
    exit 2
  fi
fi

if [ -n "$MODELS_DIR_ARG" ]; then
  DIR="$MODELS_DIR_ARG"
elif [ -n "${MODELS_DIR:-}" ]; then
  DIR="$MODELS_DIR"
else
  DIR="./assets/models"
fi

if [ "${FETCH_MODELS_ALLOW_LOCAL_URLS:-}" = "1" ]; then
  info "local URL allowlist active (test-only): file:// and http:// model URLs are accepted"
fi

if [ "$ORT_ONLY" -eq 1 ]; then
  fetch_ort
else
  fetch_models
fi
