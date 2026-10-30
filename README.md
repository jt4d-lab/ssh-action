# ssh-action
GitHub Actions for executing remote ssh commands.

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
