#!/bin/sh
# Installs tender and sets up this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- agent --hub URL --machine NAME
#   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub
#
# TENDER_VERSION=v0.1.0 installs that release instead of the latest.
# For tests only: TENDER_API_URL (the latest-release lookup), TENDER_RELEASES_URL (the downloads)
# and TENDER_BIN_DIR (where tender goes) point the script somewhere else.

set -eu

repo=g4f4r0/tender
asset=tender_linux_amd64

say() { printf '%s\n' "$*"; }
fail() { printf 'tender install: %s\n' "$*" >&2; exit 1; }

# fetch URL FILE downloads with curl, or wget when curl is missing.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsL --retry 3 -o "$2" "$1" || fail "could not download $1. Check the version and this machine's internet access, then run the command again."
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1" || fail "could not download $1. Check the version and this machine's internet access, then run the command again."
	else
		fail "curl or wget is needed to download tender. Install one (for example: apt install curl) and run the command again."
	fi
}

# ask_token reads the hub token from the terminal with echo off. The script itself arrives on stdin.
ask_token() {
	( : </dev/tty ) 2>/dev/null || fail "no terminal to ask for the hub token. Save the token in a file and add --token-file PATH."
	saved=$(stty -g </dev/tty)
	trap 'stty "$saved" </dev/tty; rm -rf "$tmp"' EXIT
	printf 'Hub token for this machine (typing is hidden): ' >/dev/tty
	stty -echo </dev/tty
	IFS= read -r token </dev/tty || token=
	stty "$saved" </dev/tty
	trap 'rm -rf "$tmp"' EXIT
	printf '\n' >/dev/tty
	[ -n "$token" ] || fail "the hub token is empty. Create one on the hub with: tender hub add-machine NAME"
}

main() {
	case "${1:-}" in
	agent | hub) ;;
	*) fail "usage: curl -fsSL https://raw.githubusercontent.com/$repo/main/install.sh | sudo sh -s -- agent --hub URL --machine NAME
       or: curl -fsSL https://raw.githubusercontent.com/$repo/main/install.sh | sudo sh -s -- hub" ;;
	esac
	[ "$(id -u)" -eq 0 ] || fail "this needs root. Run it with sudo, for example: curl -fsSL https://raw.githubusercontent.com/$repo/main/install.sh | sudo sh -s -- $*"
	[ "$(uname -s)" = Linux ] || fail "tender runs on Linux only. This machine runs $(uname -s)."
	case "$(uname -m)" in
	x86_64 | amd64) ;;
	*) fail "tender supports x86_64 machines only for now. This machine is $(uname -m)." ;;
	esac

	api=${TENDER_API_URL:-https://api.github.com/repos/$repo}
	downloads=${TENDER_RELEASES_URL:-https://github.com/$repo/releases}
	bin_dir=${TENDER_BIN_DIR:-/usr/local/bin}

	tmp=$(mktemp -d) || fail "could not make a temporary folder."
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	# The agent needs its hub token. It comes from --token-file, from a token saved by an earlier
	# run, or from a prompt; it reaches tender on stdin, never in an argument.
	token=
	if [ "$1" = agent ]; then
		have_token=
		for arg in "$@"; do
			case "$arg" in -token-file | --token-file | -token-file=* | --token-file=*) have_token=1 ;; esac
		done
		if [ -z "$have_token" ] && [ -f /etc/tender/credentials/hub-token ]; then
			say "Keeping the hub token already saved on this machine."
			have_token=1
		fi
		[ -n "$have_token" ] || ask_token
	fi

	tag=${TENDER_VERSION:-}
	if [ -z "$tag" ]; then
		say "Looking up the latest tender release..."
		fetch "$api/releases/latest" "$tmp/latest.json"
		tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
	fi
	case "$tag" in v*) ;; *) tag=v$tag ;; esac
	case "$tag" in
	v[0-9]*.[0-9]*.[0-9]* | v[0-9]*.[0-9]*.[0-9]*[-.]*) ;;
	*) fail "could not find a tender release (got \"$tag\"). Set one yourself, for example TENDER_VERSION=v0.1.0." ;;
	esac
	case "$tag" in *[!A-Za-z0-9._-]*) fail "\"$tag\" is not a release name. Use one like v0.1.0." ;; esac

	say "Installing tender $tag for Linux x86_64."
	say "  Downloading..."
	fetch "$downloads/download/$tag/$asset" "$tmp/$asset"
	fetch "$downloads/download/$tag/SHA256SUMS" "$tmp/SHA256SUMS"

	say "  Checking the checksum..."
	if ! (cd "$tmp" && grep -E "[ *]$asset\$" SHA256SUMS | sha256sum -c - >/dev/null 2>&1); then
		fail "the download does not match its checksum, so nothing was installed. Run the command again, and open an issue at https://github.com/$repo/issues if it keeps failing."
	fi

	mkdir -p "$bin_dir"
	chmod 755 "$tmp/$asset"
	mv -f "$tmp/$asset" "$bin_dir/.tender.new" || fail "could not write to $bin_dir."
	mv -f "$bin_dir/.tender.new" "$bin_dir/tender" || fail "could not install $bin_dir/tender."
	say "  Installed $bin_dir/tender."
	say ""

	printf '%s\n' "$token" | "$bin_dir/tender" install "$@" || fail "tender is installed at $bin_dir/tender, but setting up this machine failed. Fix the problem above, then run the same command again."
	say ""
	say "Done. $("$bin_dir/tender" version) is installed. Run \"tender help\" to see what it can do."
}

main "$@"
