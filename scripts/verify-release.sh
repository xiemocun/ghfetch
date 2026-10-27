#!/usr/bin/env bash
# Audit a published release the way a careful user would, and fail loudly.
#
# Why this exists: our own `--attestation=required` fails closed, so a release
# that ships without build provenance locks those users out. aquaproj/aqua
# shipped exactly that in v2.65.0 (missing multiple.intoto.jsonl) with a green
# CI and only found out from a user. Nothing in release.yml checks the
# published result, because at build time there is no published asset yet.
#
# Every check here compares content, not exit codes: `gh attestation verify`
# has returned 0 on a 404 in some versions, so we parse its JSON report and
# compare each subject digest and the source repository the bundle points at.
#
# Usage:
#   scripts/verify-release.sh v0.8.4
#   scripts/verify-release.sh --repo owner/name --mirror https://ghfast.top/ v0.8.4
#
# Environment used by the tests: VERIFY_RELEASE_FROM_DIR (read the assets from
# a directory instead of downloading them) and VERIFY_RELEASE_MIRROR.
set -euo pipefail

die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$*"; }

# Check one `gh attestation verify --format json` report against what we
# actually downloaded. Exit status 0 is not enough: the report names every
# subject in the bundle, so require each of our files by name *and* digest.
# expected_file holds one "name sha256" pair per line.
assert_attestation_json() {
  local json=$1 repo=$2 tag=$3 expected_file=$4
  jq -e . "$json" >/dev/null 2>&1 || die "attestation report is not valid JSON"

  local name sha
  while read -r name sha; do
    jq -e --arg n "$name" --arg s "$sha" '
        [ .[] | .verificationResult.statement.subject[]?
          | select(.name == $n and .digest.sha256 == $s) ] | length > 0
      ' "$json" >/dev/null \
      || die "$name: no attested subject with sha256 $sha (attested: $(jq -r --arg n "$name" \
           '[.[] | .verificationResult.statement.subject[]? | select(.name == $n) | .digest.sha256] | unique | join(", ")' "$json"))"
  done <"$expected_file"

  # Provenance must point back at the repository we asked for: a bundle that
  # merely attests *some* bytes was built by someone else.
  local want_uri="https://github.com/$repo" stray
  stray=$(jq -r --arg want "$want_uri" \
    '[.[] | .verificationResult.signature.certificate.sourceRepositoryURI]
     | map(select(. != $want)) | .[]' "$json")
  [ -z "$stray" ] || die "attestation points at '$stray', not $want_uri"

  local refs
  refs=$(jq -r '[.[] | .verificationResult.signature.certificate.sourceRepositoryRef] | unique | .[]' "$json")
  [ "$refs" = "refs/tags/$tag" ] \
    || die "attestation was built from '$refs', not refs/tags/$tag"
  ok "one bundle covers all $(wc -l <"$expected_file") assets, at $want_uri@refs/tags/$tag"
}

main() {
  local repo="xiemocun/ghfetch"
  local mirror="${VERIFY_RELEASE_MIRROR-https://ghfast.top/}"
  local from_dir="${VERIFY_RELEASE_FROM_DIR:-}"
  local tag=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --repo) repo=$2; shift 2 ;;
      --mirror) mirror=$2; shift 2 ;;
      -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
      -*) die "unknown flag $1" ;;
      *) tag=$1; shift ;;
    esac
  done
  [ -n "$tag" ] || tag="${GITHUB_REF_NAME:-}"
  [ -n "$tag" ] || die "no tag given (usage: $0 v1.2.3)"
  command -v jq >/dev/null || die "jq is required"

  local version=${tag#v}
  local release
  release=$(gh release view "$tag" --repo "$repo" --json tagName,isImmutable,assets) \
    || die "$repo has no release $tag"

  # Users who pin a tag expect the bytes behind it not to move later.
  [ "$(jq -r '.isImmutable' <<<"$release")" = "true" ] \
    || die "$repo $tag is not an immutable release: the tag can be repointed at other bytes later"
  ok "$tag is an immutable release"

  local expected="" goos goarch name
  expected="ghfetch_${version}_checksums.txt"
  for goos in linux darwin; do
    for goarch in amd64 arm64; do
      expected="$expected ghfetch_${version}_${goos}_${goarch}.tar.gz"
    done
  done
  for goarch in amd64 arm64; do
    expected="$expected ghfetch_${version}_windows_${goarch}.zip"
  done

  local actual
  actual=$(jq -r '.assets[].name' <<<"$release" | sort | tr '\n' ' ' | sed 's/ $//')
  [ "$actual" = "$(tr ' ' '\n' <<<"$expected" | sort | tr '\n' ' ' | sed 's/ $//')" ] \
    || die "asset list is not the expected 7:
  expected: $(tr ' ' '\n' <<<"$expected" | sort | tr '\n' ' ')
  actual:   $actual"
  ok "all 7 assets are present under the expected names"

  local workdir
  workdir=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$workdir'" EXIT
  local expected_file="$workdir/expected.txt"
  : >"$expected_file"

  local sums_file="$workdir/ghfetch_${version}_checksums.txt"
  for name in $expected; do
    local src="$workdir/$name"
    if [ -n "$from_dir" ]; then
      src="$from_dir/$name"
      [ -f "$src" ] || die "$name: missing from $from_dir"
    else
      curl -fsSL -o "$src" "${mirror}https://github.com/$repo/releases/download/$tag/$name" \
        || die "$name: download failed through $mirror"
    fi

    local sha
    sha=$(sha256sum "$src" | cut -d' ' -f1)

    # The API's own record of the asset digest is not served by the mirror, so
    # comparing against it catches a mirror that rewrote the file.
    local api_digest
    api_digest=$(jq -r --arg n "$name" '.assets[] | select(.name == $n) | .digest' <<<"$release")
    [ "$api_digest" = "sha256:$sha" ] \
      || die "$name: sha256 $sha does not match the digest the API records ($api_digest)"

    if [ "$name" = "ghfetch_${version}_checksums.txt" ]; then
      sums_file=$src
      ok "$name: matches the digest the API records"
    else
      # sha256sum runs in dist/ as `sha256sum ./*`, so entries are
      # `<hash>  ./<name>`; accept both forms.
      awk -v s="$sha" -v n="$name" \
        '($1 == s) && ($2 == n || $2 == "./" n) { found = 1 } END { exit !found }' "$sums_file" \
        || die "$name: not listed with sha256 $sha in the checksums file"
      ok "$name: matches the API digest and the checksums file"
    fi
    printf '%s %s\n' "$name" "$sha" >>"$expected_file"
  done

  # One call is enough: the release workflow attests dist/* in a single bundle
  # whose statement lists every subject. Doing this per asset would hammer the
  # API seven times for the same report.
  local report="$workdir/attestation.json"
  gh attestation verify "$sums_file" --repo "$repo" --format json >"$report" 2>"$workdir/err" \
    || die "gh attestation verify failed: $(tail -n 2 "$workdir/err" | tr '\n' ' ')"
  assert_attestation_json "$report" "$repo" "$tag" "$expected_file"

  printf '\nverified %s: immutable release, 7 assets, checksums and attestation agree\n' "$tag"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi