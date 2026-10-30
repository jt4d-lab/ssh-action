# ssh-action

Run a script on remote hosts over SSH, and copy local files to them, without
downloading anything at runtime.

The action is a composite step over a static Go binary that is committed in
`dist/`. `action/entrypoint.sh` picks the archive for the runner architecture,
unpacks it into `RUNNER_TEMP` and runs it with the `INPUT_*` variables of the
step. There is no `curl`, no `wget`, no checksums download and no retry loop: the
only network access of the step is the ordinary checkout of this repository,
which the runner caches per `repo@ref`. It runs in GitHub Actions and in Gitea
Actions behind a VPN alike.

## Install

```yaml
- name: Deploy over SSH
  uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: example.com
    username: deploy
    key: ${{ secrets.SSH_KEY }}
    script: |
      cd /srv/app && ./deploy.sh
```

The runner needs `sh`, `gzip` and the usual `mktemp`, `od` and `tr` of
coreutils or busybox, on linux/amd64 or linux/arm64. Nothing else: no docker, no
node, no Go. Until the first `v1.0.0` tag exists, point `uses:` at a commit or at
`@master`.

The hosts need a POSIX shell, and `tar` (GNU or busybox) when `files` is used.

## Inputs

### Connection

| Input | Default | Description |
| --- | --- | --- |
| `host` | — | Comma separated host list; every host may carry its own port as `host:port`. |
| `port` | `22` | SSH port of the hosts that have no port of their own. |
| `protocol` | `tcp` | `tcp`, `tcp4` or `tcp6`. |
| `username` | `root` | User to log in as. |
| `password` | — | Password. Use instead of `key` or `key_path`. |
| `key` | — | Private key content. Use instead of `password`. |
| `key_path` | — | Path of a private key file. Use instead of `password`. |
| `passphrase` | — | Passphrase of the private key. |
| `fingerprint` | — | Expected host key fingerprint, for example `SHA256:…`. |
| `timeout` | `30s` | SSH handshake timeout. |
| `cipher` | — | Comma separated SSH ciphers; same as `ciphers`. |
| `ciphers` | — | Comma separated SSH ciphers; same as `cipher`. |
| `use_insecure_cipher` | `false` | Allow the insecure ciphers of the SSH library. |
| `sync` | `false` | Run the hosts one after another and stop on the first failure instead of running them in parallel. |

One of `password`, `key` or `key_path` is required.

### Jump host

Every target host is reached through `proxy_host` when it is set.

| Input | Default | Description |
| --- | --- | --- |
| `proxy_host` | — | Address of the jump host. |
| `proxy_port` | `22` | SSH port of the jump host. |
| `proxy_protocol` | `tcp` | `tcp`, `tcp4` or `tcp6`. |
| `proxy_username` | — | User to log in to the jump host as. |
| `proxy_password` | — | Password of the jump host user. |
| `proxy_key` | — | Private key content for the jump host. |
| `proxy_key_path` | — | Path of a private key file for the jump host. |
| `proxy_passphrase` | — | Passphrase of the jump host private key. |
| `proxy_fingerprint` | — | Expected fingerprint of the jump host key. |
| `proxy_cipher` | — | Comma separated SSH ciphers of the jump host. |
| `proxy_use_insecure_cipher` | `false` | Allow the insecure ciphers of the SSH library on the jump host. |
| `proxy_timeout` | `30s` | SSH handshake timeout of the jump host. |

### Script

| Input | Default | Description |
| --- | --- | --- |
| `script` | — | Script to run on every host. |
| `script_path` | — | Path of a local script file to run on every host, after `script`. |
| `script_stop` | `false` | Stop the script on the first failing line and return its exit status. |
| `command_timeout` | `10m` | Time budget of a host, covering both the file upload and the script. |
| `envs` | — | Comma separated names of environment variables to export on the host. |
| `envs_format` | `export {NAME}={VALUE}` | Format of one environment export, with `{NAME}` and `{VALUE}` placeholders. |
| `allenvs` | `false` | Export every `DRONE_`, `PLUGIN_`, `INPUT_` and `GITHUB_` variable of the runner on the host. |
| `request_pty` | `false` | Request a pseudo terminal for the script. |
| `debug` | `false` | Print the configuration with the credentials redacted. |
| `capture_stdout` | `false` | Capture the remote stdout into the `stdout` output. |

### Workdir and files

`workdir`, `create_workdir` and `files` are absent upstream; `files` alone
makes a copy-only deploy.

| Input | Default | Description |
| --- | --- | --- |
| `workdir` | — | Working directory on the host, an absolute path or `~/path`. |
| `create_workdir` | `false` | Create `workdir` before running the script. |
| `files` | — | Local paths and glob patterns to copy into `workdir`, one per line. The directory is created even without `create_workdir`. |

### Outputs

| Output | Description |
| --- | --- |
| `stdout` | Remote stdout of the script, filled only when `capture_stdout` is true. |

## Exit codes

`0` — success; `1` — a failed connection, upload, command or timeout; `2` — a
broken configuration, detected before any connection is made: a missing host, no
credentials, an unknown protocol, an invalid `port` or boolean, an unparsable
duration, `workdir` of the form `~user/…`, `files` or `create_workdir` without
`workdir`, a `files` pattern that matches nothing, or an empty `script` with
neither `script_path` nor `files`.

The exit status of the remote command is not passed through: the step exits with
`1` and the status is part of the error message.

## Examples

### Password authentication

```yaml
- uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: 10.0.0.11
    username: deploy
    password: ${{ secrets.SSH_PASSWORD }}
    script: systemctl restart my-service
```

### Key authentication with a pinned host key

`fingerprint` makes the step fail when the host presents another key, which is
the point of pinning it: a reinstall of the host shows up as a failed step, not
as a silent login somewhere else.

```yaml
- uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: 10.0.0.11
    username: deploy
    key: ${{ secrets.SSH_KEY }}
    passphrase: ${{ secrets.SSH_KEY_PASSPHRASE }}
    fingerprint: SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU
    script: |
      set -e
      cd /srv/app
      ./deploy.sh --ref "$GITHUB_SHA"
```

`key_path: ~/.ssh/id_ed25519` works as well, when the runner already has the
file.

### Through a jump host

```yaml
- uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: 10.0.0.11,10.0.0.12
    username: deploy
    key: ${{ secrets.SSH_KEY }}
    proxy_host: bastion.example.com
    proxy_username: deploy
    proxy_key: ${{ secrets.BASTION_KEY }}
    command_timeout: 5m
    script: uptime
```

### Several hosts

The hosts run in parallel by default: every host starts, the step waits for all
of them and reports the first failure. With `sync: true` they run one after
another and the first failure stops the rest.

```yaml
- uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: 10.0.0.11:2222,10.0.0.12:2222
    username: deploy
    password: ${{ secrets.SSH_PASSWORD }}
    sync: true
    script_stop: true
    script: |
      cd /srv/app
      ./migrate.sh
      ./restart.sh
```

With `script_stop` a failing line ends the script with that line's exit status
instead of running the remaining ones.

### Environment variables

`envs` names the variables to read on the runner and export on the host; the name
is upper-cased and the value is always single-quoted, so a custom `envs_format`
must not quote `{VALUE}` itself. A name that is not set on the runner is skipped
with a warning; a name that is set to an empty value is exported as an empty
string.

```yaml
- uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: 10.0.0.11
    username: deploy
    password: ${{ secrets.SSH_PASSWORD }}
    envs: image_tag,registry
    script: |
      docker pull "$REGISTRY/app:$IMAGE_TAG"
      docker tag "$REGISTRY/app:$IMAGE_TAG" app:current
```

`allenvs: true` forwards every `DRONE_`, `PLUGIN_`, `INPUT_` and `GITHUB_`
variable of the runner instead. It forwards `INPUT_KEY`, `INPUT_PASSWORD`,
`INPUT_PASSPHRASE` and `GITHUB_TOKEN` too, so treat the host as trusted.

### Capturing the remote stdout

```yaml
- name: Read the deployed version
  id: version
  uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: 10.0.0.11
    username: deploy
    password: ${{ secrets.SSH_PASSWORD }}
    capture_stdout: true
    script: cat /srv/app/VERSION

- run: echo "deployed ${{ steps.version.outputs.stdout }}"
```

Only the remote stdout lands in the output: the service messages of the action
go to stderr, so the output is exactly what the command printed. A failing
command still fills the output and fails the step with exit code `1`, with the
remote status in the error message.

### Deploying artifacts

`files` copies each match of a pattern, with its own basename, into `workdir`.
Each `files` name is resolved against `GITHUB_WORKSPACE` unless it is absolute.
`filepath.Glob` does the matching, so `**` is not supported: `dist/*` matches the
entries of `dist/`, `dist/**` matches nothing and fails the step. A pattern that
matches nothing is an error, not an empty deploy.

```yaml
- uses: jt4d-lab/ssh-action@v1.0.0
  with:
    host: 10.0.0.11
    username: deploy
    password: ${{ secrets.SSH_PASSWORD }}
    workdir: /srv/app/releases/${{ github.run_number }}
    create_workdir: true
    files: |
      dist/*
      deploy/run.sh
    script: |
      ./run.sh --release "${{ github.run_number }}"
      ln -sfn /srv/app/releases/${{ github.run_number }} /srv/app/current
```

The order on every host is: connect, create `workdir`, unpack `files` into it,
then run the script — so the script sees the files it deploys. The script runs
in `workdir`, the directory exists at the time `files` are unpacked even without
`create_workdir`, and `~` and spaces in the path are fine. `files` without a
`script` copies and exits.

What is copied is what `cp -r` would copy: a matched file or directory comes
whole under its basename, a matched symlink is followed, symlinks inside a copied
directory stay symlinks, and FIFOs, sockets and devices are skipped with a
warning. Two patterns that end with the same basename overwrite each other, the
last one winning. The upload is a `tar` stream into `workdir`, so an interrupted
step can leave a partially unpacked set behind; the step fails, and the next run
unpacks into its own paths if `workdir` is versioned, as above.

## Migrating from appleboy/ssh-action

Replace the `uses:` line, keep the `with:` block:

```diff
-- uses: appleboy/ssh-action@v1.2.0
++ uses: jt4d-lab/ssh-action@v1.0.0
   with:
     host: ${{ secrets.SSH_HOST }}
     username: ${{ secrets.SSH_USER }}
     key: ${{ secrets.SSH_KEY }}
     script: ./deploy.sh
```

Input names, their defaults and their semantics are the same as upstream, so
nothing else has to change. Differences to look at:

- Drop `version` and `curl_insecure`: they existed only to download the
  `drone-ssh` binary and no longer do anything.
- `cipher` now works. Upstream passed it to the plugin as `INPUT_CIPHER` while
  `drone-ssh` read `INPUT_CIPHERS`, so the input had no effect there; this action
  accepts both spellings, so a pipeline that set `cipher` will start negotiating
  only the ciphers it names.
- `script_stop` is new here (it comes from `drone-ssh`, which upstream does not
  expose). It is not enabled, so existing scripts behave as before.
- `workdir`, `create_workdir` and `files` are new; `cd /path && …` in `script`
  keeps working.
- Configuration errors exit with `2` instead of `1`, so a broken `with:` block
  can no longer be mistaken for a failed remote command.
- Service messages of the action (the warnings about skipped `envs` names, the
  errors, the final `✅` line) go to stderr, and the `stdout` output carries the
  remote stdout only. Upstream mixed both streams into its captured stdout.
- `envs` warns about a variable that is not set on the runner and skips it
  instead of exporting an empty value.

## Deviations and limitations

Conscious differences from `appleboy/ssh-action`:

1. The inputs `version` and `curl_insecure` are gone.
2. `workdir`, `create_workdir`, `files` and `script_stop` are added.
3. Service messages of the action go to stderr; `capture_stdout` is clean.
4. A variable named in `envs` but missing on the runner is logged as a warning.
5. Configuration errors exit with `2`, not `1`.
6. One connection per host runs the upload and the script in two sessions of
   that connection; the PTY, when `request_pty` is set, belongs to the script
   session only, and the tar stream never sees it.
7. No keepalive and no `SIGTERM` handling: `timeout` bounds the handshake,
   `command_timeout` bounds the whole host, and a cancelled job ends when the
   runner kills the step process.

Limitations:

- Runners: linux/amd64 and linux/arm64 only, with `sh`, `gzip` and the
  `mktemp`/`od`/`tr` used to write the `stdout` output.
- Hosts: a POSIX shell; `tar` (GNU or busybox) for `files`.
- `workdir` is an absolute path or `~/path`; `~user/…` is rejected.
- `files` does not expand `**`.
- An upload is not atomic, see above.
- `allenvs` forwards the credentials of the step, see above.

## Releasing

The action is consumed by checking this repository out at a ref, so a release is
a tag on a commit that already carries the `dist/` built from that version;
nothing is uploaded anywhere.

1. `VER=vX.Y.Z make dist` rebuilds `dist/` with the version of the upcoming tag.
2. Commit `dist/`.
3. `git tag vX.Y.Z` and push the commit together with the tag.
4. `.github/workflows/release.yml` runs on the tag and fails unless the committed
   `dist/` is exactly what a build of the tagged source produces and the binaries
   report `vX.Y.Z`.

`make release-check` is that same byte comparison against a scratch build, so it
is the check to run locally before tagging; CI runs it on every push, which means
a stale `dist/` fails long before a release.

`sh tests/dist_test.sh` verifies the committed artifacts (checksums, ELF
architecture, static linking, version of the runner-architecture binary);
`sh tests/dist_test.sh release-check` proves the release check itself rejects a
`dist/` that was committed from another version.

### Updating dist

`dist/` is part of the source tree, so any change under `cmd/` or `internal/`
has to be followed by `make dist` in the same commit; `make release-check` is the
command that tells whether that was forgotten. The version in the binaries is the
closest tag, or `VER` when it is set, so a `dist/` built on a machine without
tags carries `dev` and `make release-check` rejects it.

## Development

`make build` compiles `bin/ssh-action`, `make test` runs `go test ./...`, and
`make lint` runs `go vet ./...` and `shellcheck` over the entrypoint and the
scripts in `tests/`. Conventions that a test or a CI step keeps in place:

- `dist/` is built with `-trimpath -buildvcs=false`, so that one source gives
  byte-identical archives in any commit; `make release-check` compares the
  committed archives against a scratch build and fails on a drift, which is what
  makes a forgotten `make dist` visible.
- `.gitea/workflows/ci.yml` is a byte-identical copy of
  `.github/workflows/ci.yml`; the `test` job fails when `cmp` finds a difference.
- The input tables of this README are checked against `action.yml` by
  `test_documentation` in `tests/entrypoint_test.sh`, so a new input needs its
  row with the default in the same commit.
- Shell code is POSIX `sh`, never `bash`, and the e2e job of the CI workflow
  runs the action itself against `testserver`, which speaks SSH through the same
  `internal/sshtest` package the unit tests use.

## Credits

The SSH connection layer — dial, jump host, ciphers, fingerprint checks, PTY,
timeouts — is [appleboy/easyssh-proxy](https://github.com/appleboy/easyssh-proxy)
(MIT, Copyright (c) 2017 Bo-Yi Wu), the same library `appleboy/drone-ssh` uses.
The input names and their semantics follow
[appleboy/drone-ssh](https://github.com/appleboy/drone-ssh) (MIT, Bo-Yi Wu).

## License

MIT, see [LICENSE](LICENSE).
