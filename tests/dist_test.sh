#!/bin/sh
# Tests of the committed dist/ artifacts: both archives are present, their
# checksums match, each one unpacks to a static ELF binary of the expected
# architecture, and the binary of the runner architecture reports the version it
# was built with.
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
dist_dir=$repo_dir/dist

# The version baked into the committed binaries is what make dist resolves: the
# closest git tag, or "dev" outside a tag. VER overrides it, as it does for the
# build, and CI passes the tag being released.
ver=${VER:-$(git -C "$repo_dir" describe --tags --abbrev=0 2>/dev/null || echo dev)}

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' 0

passed=0
failed=0

ok() {
	passed=$((passed + 1))
}

not_ok() {
	failed=$((failed + 1))
	printf 'not ok - %s\n' "$1" >&2
}

check_equal() {
	if [ "$2" = "$3" ]; then
		ok
	else
		not_ok "$1: got \"$3\", want \"$2\""
	fi
}

check_exists() {
	if [ -e "$2" ]; then
		ok
	else
		not_ok "$1: $2 does not exist"
	fi
}

check_nonempty() {
	if [ -s "$2" ]; then
		ok
	else
		not_ok "$1: $2 is empty"
	fi
}

check_static() {
	if [ -z "$static_marker" ]; then
		printf 'skip - %s: ldd is not available, static linking not checked\n' "$1" >&2
		return 0
	fi
	case $static_marker in
	*"not a dynamic executable"* | *"statically linked"*) ok ;;
	*) not_ok "$1: not a static binary: $static_marker" ;;
	esac
}

# elf_machine prints the e_machine field of an ELF header: 62 is amd64, 183 is
# arm64. Both targets are little endian.
elf_machine() {
	od -An -tu2 -j18 -N2 "$1" | tr -d ' '
}

test_artifacts_present() {
	for path in dist/checksums.txt dist/linux-amd64.gz dist/linux-arm64.gz; do
		check_exists "$path is committed" "$repo_dir/$path"
	done
}

test_checksums() {
	check_equal "checksums: entries" 2 "$(grep -c . "$dist_dir/checksums.txt")"

	set +e
	(cd "$dist_dir" && LC_ALL=C sha256sum -c checksums.txt) >"$work_dir/checksums.out" 2>&1
	status=$?
	set -e
	check_equal "checksums: sha256sum -c exit status" 0 "$status"
	check_equal "checksums: verified archives" 2 "$(grep -c ': OK$' "$work_dir/checksums.out")"
	if [ "$status" -ne 0 ]; then
		sed 's/^/    /' "$work_dir/checksums.out" >&2
	fi
}

test_archives() {
	for arch in amd64 arm64; do
		binary=$work_dir/linux-$arch
		gzip -dc "$dist_dir/linux-$arch.gz" >"$binary"
		chmod 0755 "$binary"
		check_nonempty "linux-$arch: unpacks to a binary" "$binary"
		case $arch in
		amd64) want=62 ;;
		arm64) want=183 ;;
		esac
		check_equal "linux-$arch: ELF machine" "$want" "$(elf_machine "$binary")"
	done
}

test_static_binaries() {
	for arch in amd64 arm64; do
		static_marker=
		if command -v ldd >/dev/null 2>&1; then
			set +e
			static_marker=$(LC_ALL=C ldd "$work_dir/linux-$arch" 2>&1)
			set -e
		fi
		check_static "linux-$arch"
	done
}

test_version() {
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)
		printf 'skip - version: unsupported runner architecture %s, the binaries cannot run here\n' \
			"$(uname -m)" >&2
		return 0
		;;
	esac

	set +e
	reported=$("$work_dir/linux-$arch" --version 2>&1)
	status=$?
	set -e
	check_equal "version: --version exit status" 0 "$status"
	check_equal "version: baked in version" "ssh-action $ver" "$reported"
}

test_artifacts_present
test_checksums
test_archives
test_static_binaries
test_version

printf '%s passed, %s failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ] || exit 1
