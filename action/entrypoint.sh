#!/bin/sh
# Entrypoint of the composite action: unpacks the dist binary of the runner
# architecture and runs it with the INPUT_* variables of the step.
set -eu

# fail reports a setup problem of the action itself, before any connection.
fail() {
	printf 'ssh-action: %s\n' "$*" >&2
	exit 2
}

# write_stdout_output prints the captured remote stdout as the stdout output of
# the action, between a delimiter line that cannot occur in the payload. The read
# loop keeps a payload without a final newline out of the delimiter line.
write_stdout_output() {
	delimiter="SSH_ACTION_EOF_$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
	printf 'stdout<<%s\n' "$delimiter"
	while IFS= read -r line || [ -n "$line" ]; do
		printf '%s\n' "$line"
	done <"$1"
	printf '%s\n' "$delimiter"
}

action_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

machine=$(uname -m)
case "$machine" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported architecture $machine: only linux/amd64 and linux/arm64 are supported" ;;
esac

temp_dir=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
binary=$temp_dir/ssh-action-$arch/ssh-action

if [ ! -x "$binary" ]; then
	archive=$action_dir/dist/linux-$arch.gz
	[ -f "$archive" ] || fail "missing $archive: build it with make dist"
	mkdir -p "${binary%/*}"
	gzip -dc "$archive" >"$binary"
	chmod 0755 "$binary"
fi

case $(printf '%s' "${INPUT_CAPTURE_STDOUT:-}" | tr '[:upper:]' '[:lower:]') in
true | t | 1) ;;
*) exec "$binary" "$@" ;;
esac

capture_file=$(mktemp "$temp_dir/ssh-action-stdout.XXXXXX")
SSH_ACTION_STDOUT_FILE=$capture_file
export SSH_ACTION_STDOUT_FILE

status=0
"$binary" "$@" || status=$?

if [ -z "${GITHUB_OUTPUT:-}" ]; then
	printf 'ssh-action: GITHUB_OUTPUT is not set, dropping the captured stdout\n' >&2
else
	write_stdout_output "$capture_file" >>"$GITHUB_OUTPUT"
fi
rm -f "$capture_file"
exit "$status"
