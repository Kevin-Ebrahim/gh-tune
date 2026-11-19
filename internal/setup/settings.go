package setup

import (
	"slices"
	"sort"
	"strings"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
)

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func settings(p profile.Profile) []job {
	var jobs []job
	r := p.Repository
	methods := object{}
	if r.MergeMethods != nil {
		for k, field := range map[string]string{"merge": "allow_merge_commit", "squash": "allow_squash_merge", "rebase": "allow_rebase_merge"} {
			methods[field] = slices.Contains(r.MergeMethods, k)
		}
	}
	if r.EnableMergeCommit != nil {
		methods["allow_merge_commit"] = *r.EnableMergeCommit
	}
	if len(methods) > 0 {
		jobs = append(jobs, projection("Merge methods", "", "PATCH", methods))
	}
	for _, f := range []struct {
		label, key string
		v          *bool
	}{{"Delete branches after merge", "delete_branch_on_merge", r.DeleteBranchOnMerge}, {"Issues", "has_issues", r.Issues}, {"Wiki", "has_wiki", r.Wiki}, {"Discussions", "has_discussions", r.Discussions}} {
		if f.v != nil {
			jobs = append(jobs, projection(f.label, "", "PATCH", object{f.key: *f.v}))
		}
	}
	s := p.Security
	jobs = append(jobs, securityPair(s.DependabotAlerts, s.DependabotUpdates,
		func(v bool) job { return securityToggle("Dependabot alerts", "/vulnerability-alerts", v) },
		func(v bool) job { return securityToggle("Dependabot security updates", "/automated-security-fixes", v) })...)
	jobs = append(jobs, securityPair(s.SecretScanning, s.PushProtection,
		func(v bool) job { return scanning("Secret scanning", "secret_scanning", v) },
		func(v bool) job { return scanning("Push protection", "secret_scanning_push_protection", v) })...)
	if s.PrivateReporting != nil {
		j := securityToggle("private vulnerability reporting", "/private-vulnerability-reporting", *s.PrivateReporting)
		jobs = append(jobs, publicOnly(j))
	}
	a := p.Actions
	if a.DefaultPermissions != "" {
		jobs = append(jobs, projection("workflow token permissions", "/actions/permissions/workflow", "PUT", object{"default_workflow_permissions": a.DefaultPermissions}, "can_approve_pull_request_reviews"))
	}
	if a.Allowed != "" {
		jobs = append(jobs, projection("allowed actions", "/actions/permissions", "PUT", object{"allowed_actions": a.Allowed}, "enabled"))
	}
	if a.Selected != nil {
		jobs = append(jobs, selected(*a.Selected))
	}
	if a.Approval != "" {
		jobs = append(jobs, publicOnly(projection("external contributor approval", "/actions/permissions/fork-pr-contributor-approval", "PUT", object{"approval_policy": a.Approval})))
	}
	return jobs
}

// Enable a prerequisite first; disable its dependent first when resetting both.
func securityPair(parent, child *bool, parentJob, childJob func(bool) job) []job {
	if parent == nil && child == nil {
		return nil
	}
	if parent == nil {
		return []job{childJob(*child)}
	}
	if child == nil {
		return []job{parentJob(*parent)}
	}
	p, c := parentJob(*parent), childJob(*child)
	if !*parent && !*child {
		p.after = c.name
		return []job{c, p}
	}
	if *child {
		c.after = p.name
	}
	return []job{p, c}
}
func publicOnly(j job) job {
	base := j.inspect
	j.inspect = func(s *github.Snapshot) (entry, error) {
		var r struct{ Visibility string }
		if err := s.Get("", &r); err != nil {
			return entry{}, err
		}
		switch r.Visibility {
		case "private", "internal":
			return skip("public repositories only"), nil
		case "public":
			return base(s)
		default:
			return blocked("repository visibility unknown"), nil
		}
	}
	return j
}
func securityToggle(name, path string, want bool) job {
	return setting(name, func(s *github.Snapshot) (entry, error) {
		var state struct {
			Enabled *bool
			Paused  bool
		}
		var out any = &state
		if path == "/vulnerability-alerts" {
			out = nil
		}
		err := s.Get(path, out)
		var enabled bool
		switch {
		case github.Status(err, 404) && path != "/private-vulnerability-reporting":
			// These endpoints document 404 for disabled. Confirm admin access independently.
			var r struct{ Permissions struct{ Admin bool } }
			if e := s.Get("", &r); e != nil {
				return entry{}, e
			}
			if !r.Permissions.Admin || s.Get("/actions/permissions", nil) != nil {
				return blocked("HTTP 404; cannot verify administrative read access"), nil
			}
		case err != nil:
			return entry{}, err
		case out == nil:
			enabled = true
		case state.Enabled == nil:
			return blocked("enabled state unavailable"), nil
		default:
			enabled = *state.Enabled
		}
		if enabled == want {
			if state.Paused && want {
				return blocked("enabled but paused by GitHub; inspect Dependabot activity"), nil
			}
			return match(onOff(enabled)), nil
		}
		method := "DELETE"
		if want {
			method = "PUT"
		}
		return change(onOff(enabled)+" → "+onOff(want), method, path, nil), nil
	})
}
func scanning(label, key string, want bool) job {
	return setting(label, func(s *github.Snapshot) (entry, error) {
		var repo struct {
			Security map[string]struct{ Status string } `json:"security_and_analysis"`
		}
		if err := s.Get("", &repo); err != nil {
			return entry{}, err
		}
		old := repo.Security[key].Status
		if old != "enabled" && old != "disabled" {
			return blocked("security state unavailable; check repository access, security policy and plan"), nil
		}
		status := "disabled"
		if want {
			status = "enabled"
		}
		if old == status {
			return match(onOff(want)), nil
		}
		return change(onOff(old == "enabled")+" → "+onOff(want), "PATCH", "", object{"security_and_analysis": object{key: object{"status": status}}}), nil
	})
}
func selected(want profile.Selected) job {
	want.Patterns = slices.Clone(want.Patterns)
	sort.Strings(want.Patterns)
	want.Patterns = slices.Compact(want.Patterns)
	if want.Patterns == nil {
		want.Patterns = []string{}
	}
	j := setting("selected actions", func(s *github.Snapshot) (entry, error) {
		parent, err := readObject(s, "/actions/permissions")
		if err != nil {
			return entry{}, err
		}
		if parent["allowed_actions"] != "selected" {
			return entry{state: "AFTER", detail: "Set the allowlist after selecting actions: " + describeAllowlist(want)}, nil
		}
		var old profile.Selected
		path := "/actions/permissions/selected-actions"
		if err = s.Get(path, &old); err != nil {
			return entry{}, err
		}
		sort.Strings(old.Patterns)
		if old.Patterns == nil {
			old.Patterns = []string{}
		}
		if encode(old) == encode(want) {
			return match(describeAllowlist(old)), nil
		}
		e := change(describeAllowlist(old)+" → "+describeAllowlist(want), "PUT", path, want)
		e.guard += encode(old)
		return e, nil
	})
	j.after = "allowed actions"
	return j
}

func onOff(enabled bool) string {
	if enabled {
		return "Enabled"
	}
	return "Disabled"
}

func describeSettings(values object) string {
	// Only describe the merge methods managed by this profile.
	methods := []string{}
	for _, m := range []struct{ field, label string }{{"allow_merge_commit", "Merge commits"}, {"allow_squash_merge", "Squash"}, {"allow_rebase_merge", "Rebase"}} {
		if value, ok := values[m.field].(bool); ok {
			if len(values) == 1 {
				return m.label + " " + strings.ToLower(onOff(value))
			}
			if value {
				methods = append(methods, m.label)
			}
		}
	}
	if len(methods) > 0 {
		return strings.Join(methods, ", ")
	}
	if len(values) > 1 {
		return "None enabled"
	}
	for _, v := range values {
		switch v := v.(type) {
		case bool:
			return onOff(v)
		case string:
			switch v {
			case "read":
				return "Read only"
			case "write":
				return "Read and write"
			case "all":
				return "All actions"
			case "local_only":
				return "Local actions only"
			case "selected":
				return "Selected actions"
			case "all_external_contributors":
				return "All external contributors"
			case "first_time_contributors":
				return "First-time contributors"
			case "first_time_contributors_new_to_github":
				return "First-time contributors new to GitHub"
			}
			return v
		}
	}
	return encode(values)
}
func describeAllowlist(a profile.Selected) string {
	var allowed []string
	if a.GithubOwned {
		allowed = append(allowed, "GitHub-owned actions")
	}
	if a.Verified {
		allowed = append(allowed, "verified creators")
	}
	allowed = append(allowed, a.Patterns...)
	if len(allowed) == 0 {
		return "No additional actions"
	}
	return strings.Join(allowed, ", ")
}
