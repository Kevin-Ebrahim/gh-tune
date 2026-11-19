# gh-tune

Apply and check GitHub repository settings with reusable profiles.

## Quick start

Create a repository on GitHub, then run these commands from any directory. Sign in with `gh auth login` if this is your first time using GitHub CLI.

```sh
gh extension install Kevin-Ebrahim/gh-tune
gh tune OWNER/REPO --profile solo --dry-run
gh tune OWNER/REPO --profile solo --apply
gh tune OWNER/REPO --profile solo --check
```

Dry-run is the default. It reads the current settings, shows proposed changes, and previews the contents of new files. Apply checks the current state again before each write and reports the outcome. Check is read-only and is suitable for scripts.

| Profile | Default branch | Repository files |
| --- | --- | --- |
| [default](internal/profile/profiles/default.yaml) | Remove the standard gh-tune ruleset | Settings only |
| [solo](internal/profile/profiles/solo.yaml) | Protect against force pushes and deletion. Direct commits are fine. | Settings only |
| [team](internal/profile/profiles/team.yaml) | Also require a PR and one approval. | Starter PR template |
| [opensource](internal/profile/profiles/opensource.yaml) | Same review requirements as team. | Starter issue and PR templates |

The solo, team, and opensource profiles enable merge commits, preserve existing squash and rebase choices, delete merged branches, enable Issues, request Dependabot alerts and security updates, request secret scanning and push protection, and set the default workflow token to read. The opensource profile also enables Discussions and, on public repositories, private vulnerability reporting and maintainer approval for workflow runs from all external contributors.

Branch requirements are minimums. A branch already requiring two approvals satisfies a profile asking for one. Branch rules include any bypasses configured by the repository or organization. gh-tune adds missing protections in a repository ruleset named `gh-tune: default branch`. Reserve this name for the extension. These three profiles keep stronger protections and leave Wiki and Actions allowlists opt-in.

## Restore GitHub creation defaults

```sh
gh tune OWNER/REPO --profile default --dry-run
gh tune OWNER/REPO --profile default --apply
```

`default` uses the baseline for a newly created standalone GitHub.com repository for the settings gh-tune manages. It enables merge, squash, and rebase, keeps merged branches, enables Issues and Wiki where supported, and disables Discussions. Existing files, PRs, classic branch protection, and other rulesets stay in place. The preview identifies the gh-tune ruleset by name and ID before removal. A customized ruleset with additional restrictions is reported for review.

On a personal public repository, the profile keeps secret scanning and push protection on, turns Dependabot alerts, automatic security updates, and private vulnerability reporting off, allows all Actions, uses a read-only workflow token, and requires approval for first-time external contributors. These are stock settings. Personal account preferences for new repositories can differ. Applying `default` sets today's baseline, even for settings you changed before using gh-tune. [Public repository security defaults](https://github.blog/changelog/2024-03-11-secret-scanning-and-push-protection-are-enabled-by-default-on-new-public-repositories/) · [Personal security preferences](https://docs.github.com/en/account-and-profile/how-tos/account-settings/managing-security-and-analysis-features)

For an organization, gh-tune reads the default security configuration for the repository visibility and the organization's Actions policies. If a policy is unreadable, that part is reported as blocked while independent settings can still be restored. [Organization security configurations](https://docs.github.com/en/rest/code-security/configurations#get-default-code-security-configurations-for-an-organization) · [Actions permissions](https://docs.github.com/en/rest/actions/permissions)

## Custom profiles

Copy a built-in profile or start with a small YAML file. Omitted settings stay unmanaged. Custom profiles are standalone and use the same schema as the built-ins.

```yaml
version: 1
repository:
  merge_methods: [merge, squash]
  wiki: false
files:
  dependabot:
    - package-ecosystem: gomod
      directory: /
```

```sh
gh tune OWNER/REPO --profile ./library.yaml
```

- `repository` accepts `issues`, `wiki`, `discussions`, `delete_branch_on_merge`, and either `enable_merge_commit: true` or an exact `merge_methods` list containing one or more of `merge`, `squash`, and `rebase`.
- `security` accepts booleans for `dependabot_alerts`, `dependabot_security_updates`, `secret_scanning`, `push_protection`, and `private_vulnerability_reporting`.
- `branch` accepts `target: default`, `block_force_pushes`, `block_deletion`, `require_pull_request`, and `approvals` (0–10 with PRs required). False means the profile requires no minimum for that rule.
- `actions` accepts `default_workflow_permissions` (`read` or `write`), `external_contributor_approval` (`first_time_contributors_new_to_github`, `first_time_contributors`, or `all_external_contributors`), and `allowed_actions` (`all`, `local_only`, or `selected`). With `selected`, provide a `selected_actions` mapping with `github_owned`, `verified`, and `patterns`. This mapping describes the complete allowlist.
- `files` accepts `issue_template`, `pull_request_template`, and weekly Dependabot version updates. Each Dependabot entry needs an explicit package ecosystem and repository-relative directory starting with `/`. The supported ecosystem names are listed in [profile validation](internal/profile/profile.go).

## Apply and check results

GitHub settings take effect during apply. Starter files are proposed together in a PR. Existing repository templates and issue forms count as present, including customized ones. An existing Dependabot file that needs different settings is reported for manual review. Generated files are limited to templates and opted-in Dependabot configuration.

A second apply reuses a matching open PR. After a merge, check reads the files on the default branch. A closed, unmerged proposal or an unexpected change on the setup branch is reported for review. Empty repositories need a first commit before GitHub can accept a file PR.

| Exit | Meaning |
| --- | --- |
| 0 | Check found a match, dry-run completed, or apply completed its settings and file proposals |
| 1 | Invalid input, incomplete assessment, permission or plan restriction, or failed operation |
| 2 | Check found drift or a file PR awaiting merge |

The report names the target profile and groups results by outcome. Changed settings show the current and desired values. In a terminal, a spinner runs while gh-tune reads or applies settings. Set `GH_SPINNER_DISABLED=1` for a static progress message or `NO_COLOR=1` for plain text. Piped output omits animation and color.

Independent changes can succeed during a partially blocked apply. Read the per-setting results and rerun after resolving the blocker. Public-only settings appear as N/A on private repositories. A blocked requirement takes precedence over drift in the exit code.

## Authentication and releases

gh-tune uses your GitHub CLI credentials or `GH_TOKEN` for GitHub.com. The token needs access to the target repository and permissions for the requested settings. Administrative changes generally require administration write permission. Opening a file PR requires contents and pull request write permissions. Organization policies and GitHub plans can restrict individual features.

Upgrade with `gh extension upgrade tune`. Version tags trigger the extension's release workflow, which publishes precompiled binaries including Linux, macOS, and Windows. To build locally, run `go test ./...` and `go build -o gh-tune .` (use `gh-tune.exe` on Windows).
