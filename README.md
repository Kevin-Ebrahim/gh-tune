# gh-tune

gh-tune is a GitHub CLI extension. Use a profile to preview changes to a GitHub.com repository, apply them, or check whether the repository still matches.

## Quick start

Sign in with `gh auth login` or set `GH_TOKEN`. Install the extension and preview the `solo` profile on an existing repository. Replace `OWNER/REPO` with its GitHub name.

```sh
gh extension install Kevin-Ebrahim/gh-tune
gh tune OWNER/REPO --profile solo
```

The preview is read-only. Add `--apply` to make the changes.

```sh
gh tune OWNER/REPO --profile solo --apply
```

Settings take effect immediately. If the profile calls for missing files, gh-tune opens one pull request for them. It keeps existing templates and reuses a matching open pull request. The repository needs a first commit before gh-tune can propose files.

## Choose a profile

| Profile | What it does |
| --- | --- |
| [solo](internal/profile/profiles/solo.yaml) | Blocks force pushes and deletion on the default branch. Allows direct commits if existing rules permit them. |
| [team](internal/profile/profiles/team.yaml) | Uses the `solo` settings and requires a pull request with at least one approval. Proposes a pull request template. |
| [opensource](internal/profile/profiles/opensource.yaml) | Extends `team` with an issue template and Discussions. On public repositories, it also enables private vulnerability reporting and requires approval for workflow runs from all external contributors. |
| [default](internal/profile/profiles/default.yaml) | Applies GitHub's creation defaults to the settings gh-tune manages, even if you changed them before installing it. |

`solo`, `team`, and `opensource` also enable merge commits and Issues, delete merged branches, and set the default workflow token to read-only. They request Dependabot alerts and security updates, secret scanning, and push protection. Stronger branch rules still apply.

Preview `default` before applying it. Defaults depend on the owner, visibility, and organization policies. This profile can remove the `gh-tune: default branch` ruleset. It flags customized rulesets with that name for review and keeps other rulesets and existing files.

## Use a custom profile

Save the settings you want to manage in a YAML file such as `library.yaml`. gh-tune leaves omitted settings alone. This example enables Issues and deletes branches after merge.

```yaml
version: 1
repository:
  issues: true
  delete_branch_on_merge: true
```

```sh
gh tune OWNER/REPO --profile ./library.yaml
```

The [profile definition](internal/profile/profile.go) lists the supported fields and values.

## Check a repository

```sh
gh tune OWNER/REPO --profile solo --check
```

Use the exit code in scripts. `0` means the repository matches. `2` means a setting or file differs, or a file pull request still needs to merge. `1` means the check failed or could not assess every requirement, even if it also found differences.

If permissions or GitHub policies block part of an apply, other settings can still change. Review the results, resolve the blocked items, and rerun the command.
