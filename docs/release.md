# Releasing kei-cli

This is the runbook for publishing a kei-cli release. The
[README](../README.md#release-process) covers what the workflow does and what
it publishes. This page covers how to run it and what to do when it goes
wrong.

## 1. Prerequisites

Check these before you cut a release:

- **Permissions.** To push a tag you need write access to
  `HaikeiLabs/kei-cli`. To dispatch a release manually you need `admin`, which
  means a repository or organization owner.
- **`gh` authenticated** against github.com (`gh auth status`).
- **`main` is green** and contains everything you intend to ship.
- **Release runner online.** The self-hosted `kei-cli-release` runner must be
  registered and idle. Check
  <https://github.com/HaikeiLabs/kei-cli/settings/actions/runners>, or ask
  whoever owns the release runner Pod.
- **Signing key present.** The `release` environment must have the
  `GORELEASER_SIGNING_KEY` secret: the GPG key for
  `kei-cli-releases@haikeilabs.com`.
- **Bundled proxy exists.** The kei-proxy version the release will bundle
  must already be published at
  `s3://kei-cli-releases/kei-proxy/<version>/`. That is the
  `KEI_PROXY_VERSION` repository variable, or `v0.1.0` when it is unset. To
  ship a newer proxy, set the variable first:

  ```sh
  gh variable set KEI_PROXY_VERSION -R HaikeiLabs/kei-cli --body v0.1.11
  ```

- **Pick the version.** Find the current stable version with
  `git fetch --tags origin && git tag -l 'v*' --sort=-v:refname | head -3`
  or `curl -fsSL https://kei-cli-releases.s3.us-east-1.amazonaws.com/kei-cli/latest.txt`.
  Tags that contain `-rc.`, `-alpha.`, or `-beta.` are prereleases. Any other
  suffix is treated as **stable** and moves `latest.txt`.

## 2. Cut the release

Use **one** of the two paths.

### Path A: push a tag (default)

```sh
git fetch origin
git tag vX.Y.Z origin/main
git push origin vX.Y.Z
```

Use a lightweight or annotated tag; either works. Tag `origin/main`, not your
local branch, so you don't ship unpushed commits.

### Path B: manual dispatch (owners only)

```sh
gh workflow run release.yaml -R HaikeiLabs/kei-cli -f bump=patch   # or minor, major
```

The workflow finds the highest stable `vX.Y.Z` tag and bumps it (default
`patch`). It then creates and pushes the new tag on the commit it is running
from, normally `main`, and releases it in the same run. That tag push is made
with `GITHUB_TOKEN`, so it does not start a second release run. If you are
not an owner, the **Authorize manual release** job fails and nothing else
runs.

## 3. Watch the run

```sh
gh run list -R HaikeiLabs/kei-cli -w release.yaml -L 3
gh run watch -R HaikeiLabs/kei-cli <run-id> --exit-status
```

The job results you should expect:

| Path | Authorize manual release | Validate tag | Release |
|---|---|---|---|
| Tag push | skipped (expected) | success | success |
| Dispatch by an owner | success | success | success |
| Dispatch by a non-owner | **failure** | skipped | skipped |

**Check the Release job itself, not the run's overall status.** A run whose
Release job was skipped still shows green. That is how v0.1.6's tag push
looked before #44.

## 4. Verify the release

```sh
BASE=https://kei-cli-releases.s3.us-east-1.amazonaws.com/kei-cli
V=X.Y.Z                                           # no leading v
curl -fsSL "$BASE/latest.txt"; echo               # stable only: should print $V

A="kei-cli_${V}_macOS_arm64.tar.gz"               # or _macOS_x86_64, _Linux_arm64, _Linux_x86_64
curl -fsSLO "$BASE/$V/$A"
curl -fsSLO "$BASE/$V/kei-cli_${V}_checksums.txt"
curl -fsSLO "$BASE/$V/kei-cli_${V}_checksums.txt.sig"
shasum -a 256 -c --ignore-missing "kei-cli_${V}_checksums.txt"   # Linux: sha256sum -c --ignore-missing
# If you have the kei-cli-releases@haikeilabs.com public key imported:
gpg --verify "kei-cli_${V}_checksums.txt.sig" "kei-cli_${V}_checksums.txt"
```

For an end-to-end check, install into a scratch directory and check the
version:

```sh
curl -fsSL "$BASE/install.sh" | bash -s -- -v "$V" -d "$(mktemp -d)"
```

The installer prints `Installed kei <version> to <dir>/kei`. Run
`<dir>/kei --version` to confirm the version.

## 5. If something goes wrong

### Release job skipped

- **Validate tag failed.** Read its log. Usually the tag is not
  `vMAJOR.MINOR.PATCH[-prerelease]`, or on a dispatch the calculated tag
  already exists. For a bad pushed tag, delete it
  (`git push origin :refs/tags/<tag>` and `git tag -d <tag>`) and push a
  correct one.
- **Authorize manual release failed.** The person who dispatched is not an
  owner. Ask an owner to dispatch, or use Path A.
- **Everything except Authorize manual release was skipped on a tag push.**
  That is the bug fixed in #44. Make sure the tagged commit contains #44.
  The workflow runs from the tagged commit's copy of `release.yaml`, so a tag
  on an older commit still has the bug. Retag a commit that includes the fix,
  or dispatch.

### Release job failed

Find the failing step in the log:

| Step | Likely cause | Fix |
|---|---|---|
| Create and push release tag | Tag appeared since validation, or token lacks `contents: write` | Re-dispatch; check the tag list |
| Import GPG signing key / Verify GPG key | `GORELEASER_SIGNING_KEY` missing, expired, or not for `kei-cli-releases@haikeilabs.com` | Fix the secret in the `release` environment |
| Validate runner AWS identity | Runner Pod lost its IRSA role | Fix the runner deployment |
| Validate S3 bucket accessibility | Role lacks `s3:ListBucket` on the `kei-cli/` prefix | Fix the IAM policy |
| Validate pinned kei-proxy artifact prefix | `KEI_PROXY_VERSION` points at a proxy release that does not exist | Publish that proxy or change the variable |
| Run Goreleaser | Build error, or upload denied | Read the GoReleaser output |
| Verify no credentials leaked | A credential pattern appeared in checksums or `install.sh` | **Stop.** Treat it as an incident before retrying |

A failure before **Run Goreleaser** publishes nothing. Fix the cause, then
retry:

- **Tag push:** rerun the failed job from the run page, or with
  `gh run rerun <run-id> --failed -R HaikeiLabs/kei-cli`.
- **Dispatch:** if the log shows the tag was pushed, don't rerun. The rerun
  fails at *Create and push release tag* because the tag already exists.
  Don't dispatch on top of it either: that bumps to the next version and
  leaves a gap. Delete the tag, then dispatch again, which calculates the
  same version:

  ```sh
  git push origin :refs/tags/vX.Y.Z
  gh workflow run release.yaml -R HaikeiLabs/kei-cli -f bump=patch
  ```

  Alternatively, delete the tag and re-push it yourself (Path A). A
  re-pushed tag triggers a normal tag-push release.

A failure during or after **Run Goreleaser** may leave a partial
`kei-cli/<version>/` directory in S3. `latest.txt` is written last, so users
installing "latest" are not affected. Retrying the same version as described
above overwrites the same keys. If you would rather abandon the version,
release the next patch instead of reusing it.

## History

Before #44 (merged 2026-09-26), the Release job had `needs: [validate-tag]` and
no `if:`. A job without `if:` gets an implicit `success()`, which checks the
whole `needs` chain. On a tag push, Authorize manual release is skipped, so
Release was skipped too. Pushing a tag ran Validate tag, skipped Release, and
still showed a green run, so in practice releases went out only through
manual dispatch. v0.1.6 was first pushed as a tag. When its run was green
but published nothing, the tag was deleted and the version was released by
dispatch. #44 added
`if: ${{ always() && needs.validate-tag.result == 'success' }}` to Release.
It also limited Validate tag to accepting a skipped authorization only on
push events, so a dispatch still requires an owner.
