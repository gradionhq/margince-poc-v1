#!/usr/bin/env bash
# gitleaks-pin.sh — the pinned secret scanner, resolved the same way everywhere.
#
# Sourced by scripts/secret-scan.sh and scripts/test-secret-scan.sh. It does not
# scan anything itself; it hands back the path to ONE known binary.
#
# The scanner is fetched and checksum-verified rather than taken from PATH. A
# secret scan's verdict is a function of its rule set, so "some gitleaks" on a
# laptop and a pinned one in CI are not the same gate — and the difference shows
# up as a finding that only appears after you push, or worse, only in CI's
# absence. Pinning here is what lets `make secret-scan` promise the answer the
# pull request will get. It also means no engineer has to install anything: the
# repo's own toolchain rule (see the SBOM lane's digest-pinned images).
#
# Bumping the version: change GITLEAKS_VERSION and replace all four digests
# from the release's checksums.txt. All four, not just yours — the others are
# what CI and your colleagues run.

GITLEAKS_VERSION="8.30.1"

# sha256 of each release tarball, from
# https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_checksums.txt
GITLEAKS_SHA256_darwin_arm64="b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5"
GITLEAKS_SHA256_darwin_x64="dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709"
GITLEAKS_SHA256_linux_arm64="e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080"
GITLEAKS_SHA256_linux_x64="551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"

# gitleaks_platform — the release's name for this host, e.g. darwin_arm64.
gitleaks_platform() {
	local os arch
	case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*)
		echo "gitleaks-pin: unsupported OS $(uname -s)" >&2
		return 1
		;;
	esac
	case "$(uname -m)" in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=x64 ;;
	*)
		echo "gitleaks-pin: unsupported architecture $(uname -m)" >&2
		return 1
		;;
	esac
	printf '%s_%s' "$os" "$arch"
}

# gitleaks_bin — print the path to the pinned binary, downloading it once into
# .tmp/ (gitignored) if it is not already there. Every later run is a cache hit.
gitleaks_bin() {
	local root platform digest dest url tarball
	root="$(git rev-parse --show-toplevel)"
	platform="$(gitleaks_platform)" || return 1

	# Indirect expansion: pick this platform's digest out of the four above.
	local digest_var="GITLEAKS_SHA256_${platform}"
	digest="${!digest_var:-}"
	if [[ -z "$digest" ]]; then
		echo "gitleaks-pin: no pinned digest for $platform" >&2
		return 1
	fi

	dest="$root/.tmp/gitleaks/$GITLEAKS_VERSION/gitleaks"
	if [[ -x "$dest" ]]; then
		printf '%s' "$dest"
		return 0
	fi

	mkdir -p "$(dirname "$dest")"
	url="https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_${platform}.tar.gz"
	tarball="$(mktemp)"
	echo "gitleaks-pin: fetching gitleaks v$GITLEAKS_VERSION ($platform)" >&2
	# --proto/--proto-redir '=https': the release URL redirects to a CDN, and a
	# redirect to plain http would fetch the scanner over a tamperable channel.
	# The digest below would still catch a swap; the download refuses to leave
	# TLS in the first place.
	if ! curl -fsSL --proto '=https' --proto-redir '=https' -o "$tarball" "$url"; then
		rm -f "$tarball"
		echo "gitleaks-pin: could not download $url" >&2
		return 1
	fi

	if ! printf '%s  %s\n' "$digest" "$tarball" | shasum -a 256 -c - >/dev/null 2>&1; then
		rm -f "$tarball"
		echo "gitleaks-pin: checksum mismatch for gitleaks v$GITLEAKS_VERSION ($platform)." >&2
		echo "  The download did not match the digest pinned in scripts/gitleaks-pin.sh." >&2
		echo "  Do not bypass this: it means the artifact changed, not that the pin is stale." >&2
		return 1
	fi

	tar xzf "$tarball" -C "$(dirname "$dest")" gitleaks
	rm -f "$tarball"
	chmod +x "$dest"
	printf '%s' "$dest"
}
