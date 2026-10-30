#!/bin/sh
# Tests of action/entrypoint.sh: architecture selection, unpacking of the dist
# archive, the capture_stdout output and the input mapping of action.yml. Every
# case builds a throwaway copy of the action tree, so the committed dist archive
# is not needed and nothing touches the repository.
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
entrypoint=$repo_dir/action/entrypoint.sh

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

# check_status compares the exit status of the last run_entrypoint with $2.
check_status() {
	if [ "$status" -eq "$2" ]; then
		ok
	else
		not_ok "$1: exit status $status, want $2"
		printf '  stdout:\n' >&2
		sed 's/^/    /' "$case_dir/stdout" >&2
		printf '  stderr:\n' >&2
		sed 's/^/    /' "$case_dir/stderr" >&2
	fi
}

check_equal() {
	if [ "$2" = "$3" ]; then
		ok
	else
		not_ok "$1: got \"$3\", want \"$2\""
	fi
}

check_contains() {
	if grep -qF -- "$3" "$2"; then
		ok
	else
		not_ok "$1: $2 does not contain \"$3\""
		sed 's/^/    /' "$2" >&2
	fi
}

check_exists() {
	if [ -e "$2" ]; then
		ok
	else
		not_ok "$1: $2 does not exist"
	fi
}

check_missing() {
	if [ ! -e "$2" ]; then
		ok
	else
		not_ok "$1: $2 still exists"
	fi
}

# fake_binary prints the source of the stand-in for the real binary: it reports
# the architecture it was packed for, mimics the SSH_ACTION_STDOUT_FILE write and
# exits with FAKE_EXIT_CODE.
fake_binary() {
	cat <<EOF
#!/bin/sh
printf 'arch $1\n'
if [ -n "\${SSH_ACTION_STDOUT_FILE:-}" ]; then
	printf '%s' "\$SSH_ACTION_STDOUT_FILE" >"\${FAKE_STDOUT_RECORD:-/dev/null}"
	printf '%b' "\${FAKE_STDOUT:-remote one\nremote two\n}" >"\$SSH_ACTION_STDOUT_FILE"
fi
exit "\${FAKE_EXIT_CODE:-0}"
EOF
}

# new_case prepares $case_dir: a copy of the action tree with the dist archives of
# both architectures and a fake uname reporting $2 as the machine.
new_case() {
	case_dir=$work_dir/$1
	mkdir -p "$case_dir/action" "$case_dir/dist" "$case_dir/bin" "$case_dir/tmp" "$case_dir/home"
	cp "$entrypoint" "$case_dir/action/entrypoint.sh"
	for arch in amd64 arm64; do
		fake_binary "$arch" >"$case_dir/dist/linux-$arch"
		gzip -9n -c "$case_dir/dist/linux-$arch" >"$case_dir/dist/linux-$arch.gz"
		rm -f "$case_dir/dist/linux-$arch"
	done
	printf '#!/bin/sh\nprintf "%%s\\n" "%s"\n' "$2" >"$case_dir/bin/uname"
	chmod 0755 "$case_dir/bin/uname"
}

# run_entrypoint runs the copied entrypoint in a clean environment with the given
# assignments and stores its exit status in $status.
run_entrypoint() {
	set +e
	env -i PATH="$case_dir/bin:$PATH" HOME="$case_dir/home" TMPDIR="$case_dir/tmp" "$@" \
		sh "$case_dir/action/entrypoint.sh" >"$case_dir/stdout" 2>"$case_dir/stderr"
	status=$?
	set -e
}

test_architecture_selection() {
	new_case arch-amd64 x86_64
	run_entrypoint RUNNER_TEMP="$case_dir/runner"
	check_status "amd64: exit status" 0
	check_contains "amd64: the amd64 binary ran" "$case_dir/stdout" "arch amd64"
	check_exists "amd64: binary unpacked into RUNNER_TEMP" "$case_dir/runner/ssh-action-amd64/ssh-action"
	check_missing "amd64: arm64 binary not unpacked" "$case_dir/runner/ssh-action-arm64"

	new_case arch-arm64 aarch64
	run_entrypoint RUNNER_TEMP="$case_dir/runner"
	check_status "arm64: exit status" 0
	check_contains "arm64: the arm64 binary ran" "$case_dir/stdout" "arch arm64"
	check_exists "arm64: binary unpacked into RUNNER_TEMP" "$case_dir/runner/ssh-action-arm64/ssh-action"
	check_missing "arm64: amd64 binary not unpacked" "$case_dir/runner/ssh-action-amd64"
}

test_unknown_architecture() {
	new_case arch-unknown riscv64
	run_entrypoint RUNNER_TEMP="$case_dir/runner"
	check_status "unknown arch: exit status" 2
	check_contains "unknown arch: stderr names the machine" "$case_dir/stderr" "riscv64"
	check_missing "unknown arch: nothing unpacked" "$case_dir/runner"
}

test_missing_archive() {
	new_case missing-archive x86_64
	rm -f "$case_dir/dist/linux-amd64.gz"
	run_entrypoint RUNNER_TEMP="$case_dir/runner"
	check_status "missing archive: exit status" 2
	check_contains "missing archive: stderr names make dist" "$case_dir/stderr" "make dist"
	check_missing "missing archive: binary not created" "$case_dir/runner/ssh-action-amd64/ssh-action"
}

test_default_temp_dir() {
	new_case temp-fallback x86_64
	run_entrypoint
	check_status "default temp: exit status without RUNNER_TEMP" 0
	check_exists "default temp: binary under TMPDIR" "$case_dir/tmp/ssh-action-amd64/ssh-action"
}

test_second_run_reuses_binary() {
	new_case rerun x86_64
	run_entrypoint RUNNER_TEMP="$case_dir/runner"
	check_status "rerun: first exit status" 0
	printf '#!/bin/sh\nprintf "reused binary\\n"\n' >"$case_dir/runner/ssh-action-amd64/ssh-action"
	run_entrypoint RUNNER_TEMP="$case_dir/runner"
	check_status "rerun: second exit status" 0
	check_contains "rerun: the unpacked binary is reused" "$case_dir/stdout" "reused binary"
}

test_capture_stdout() {
	new_case capture x86_64
	: >"$case_dir/output"
	run_entrypoint RUNNER_TEMP="$case_dir/runner" GITHUB_OUTPUT="$case_dir/output" \
		INPUT_CAPTURE_STDOUT=true FAKE_STDOUT_RECORD="$case_dir/record"
	check_status "capture: exit status" 0
	check_contains "capture: binary stdout stays live" "$case_dir/stdout" "arch amd64"
	check_contains "capture: remote stdout in GITHUB_OUTPUT" "$case_dir/output" "remote one"
	check_equal "capture: value lines of GITHUB_OUTPUT" 4 "$(wc -l <"$case_dir/output")"
	delimiter=$(sed -n '1s/^stdout<<//p' "$case_dir/output")
	case $delimiter in
	SSH_ACTION_EOF_?*) ok ;;
	*) not_ok "capture: GITHUB_OUTPUT starts with a random delimiter line, got \"$delimiter\"" ;;
	esac
	check_equal "capture: value closed by the delimiter" \
		"$delimiter" "$(tail -n 1 "$case_dir/output")"
	check_exists "capture: binary got SSH_ACTION_STDOUT_FILE" "$case_dir/record"
	check_missing "capture: capture file removed" "$(cat "$case_dir/record")"
}

test_capture_stdout_keeps_failure() {
	new_case capture-failure x86_64
	: >"$case_dir/output"
	run_entrypoint RUNNER_TEMP="$case_dir/runner" GITHUB_OUTPUT="$case_dir/output" \
		INPUT_CAPTURE_STDOUT=TRUE FAKE_STDOUT_RECORD="$case_dir/record" FAKE_EXIT_CODE=3
	check_status "capture failure: binary exit status kept" 3
	check_contains "capture failure: GITHUB_OUTPUT written" "$case_dir/output" "remote two"
	check_missing "capture failure: capture file removed" "$(cat "$case_dir/record")"
}

test_capture_stdout_without_final_newline() {
	new_case capture-raw x86_64
	: >"$case_dir/output"
	run_entrypoint RUNNER_TEMP="$case_dir/runner" GITHUB_OUTPUT="$case_dir/output" \
		INPUT_CAPTURE_STDOUT=true FAKE_STDOUT='raw output'
	check_status "capture raw: exit status" 0
	check_contains "capture raw: payload written" "$case_dir/output" "raw output"
	check_equal "capture raw: value closed by the delimiter" \
		"$(sed -n '1s/^stdout<<//p' "$case_dir/output")" "$(tail -n 1 "$case_dir/output")"
}

test_capture_disabled() {
	new_case no-capture x86_64
	: >"$case_dir/output"
	run_entrypoint RUNNER_TEMP="$case_dir/runner" GITHUB_OUTPUT="$case_dir/output" \
		INPUT_CAPTURE_STDOUT=false FAKE_STDOUT_RECORD="$case_dir/record"
	check_status "no capture: exit status" 0
	check_equal "no capture: GITHUB_OUTPUT untouched" 0 "$(wc -c <"$case_dir/output")"
	check_missing "no capture: SSH_ACTION_STDOUT_FILE not set" "$case_dir/record"
}

test_action_definition() {
	check_contains "action.yml: composite runtime" "$repo_dir/action.yml" "using: composite"
	check_contains "action.yml: entrypoint runs with sh" "$repo_dir/action.yml" "shell: sh"
	check_contains "action.yml: stdout output comes from the step" \
		"$repo_dir/action.yml" "steps.ssh.outputs.stdout"

	from_action=$(grep -oE 'INPUT_[A-Z0-9_]+' "$repo_dir/action.yml" | sort -u)
	from_config=$(grep -oE 'INPUT_[A-Z0-9_]+' "$repo_dir/internal/config/config.go" | sort -u)
	check_equal "action.yml: every config input is mapped" "$from_config" "$from_action"

	inputs=$(printf '%s\n' "$from_config" | grep -c .)
	mapped=$(grep -c "INPUT_[A-Z0-9_]*: *\${{ inputs\.[a-z0-9_]* }}" "$repo_dir/action.yml")
	check_equal "action.yml: every input has an env mapping" "$inputs" "$mapped"

	mismatched=$(awk '/^[[:space:]]*INPUT_[A-Z0-9_]+:[[:space:]]/ {
		key = $1
		sub(/:$/, "", key)
		name = $3
		sub(/^inputs\./, "", name)
		if (key != toupper("INPUT_" name)) print key " <- " name
	}' "$repo_dir/action.yml")
	check_equal "action.yml: env name matches the input name" "" "$mismatched"
}

test_architecture_selection
test_unknown_architecture
test_missing_archive
test_default_temp_dir
test_second_run_reuses_binary
test_capture_stdout
test_capture_stdout_keeps_failure
test_capture_stdout_without_final_newline
test_capture_disabled
test_action_definition

printf '%s passed, %s failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ] || exit 1
