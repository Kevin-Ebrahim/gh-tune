package setup

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
)

type repositoryContext struct {
	Archived, Disabled bool
	Visibility         string
	Owner              struct{ Login, Type string }
	Wiki               bool                               `json:"has_wiki"`
	Security           map[string]struct{ Status string } `json:"security_and_analysis"`
}

func unavailable(name string, err error) job {
	return setting(name, func(*github.Snapshot) (entry, error) { return blocked(err.Error()), nil })
}
func creationDefaults(s *github.Snapshot, r repositoryContext, target string) []job {
	if (r.Owner.Type != "User" && r.Owner.Type != "Organization") || r.Owner.Login == "" || (r.Visibility != "public" && r.Visibility != "private" && r.Visibility != "internal") {
		return []job{unavailable("creation defaults", fmt.Errorf("cannot determine repository owner and visibility"))}
	}
	var p profile.Profile
	p.Repository.MergeMethods = []string{"merge", "squash", "rebase"}
	p.Repository.DeleteBranchOnMerge = new(false)
	p.Repository.Issues = new(true)
	p.Repository.PullRequests = new(true)
	p.Repository.IssueCreationPolicy = "all"
	p.Repository.PullRequestCreationPolicy = "all"
	p.Repository.Discussions = new(false)
	jobs := settings(p)
	jobs = append(jobs, defaultWiki(r))
	jobs = append(jobs, defaultSecurity(s, r)...)
	jobs = append(jobs, defaultActions(s, r)...)
	return append(jobs, removeManagedRuleset(target))
}
func defaultWiki(r repositoryContext) job {
	return setting("Wiki", func(s *github.Snapshot) (entry, error) {
		want := true
		if r.Visibility != "public" && !r.Wiki {
			// A disabled wiki alone does not distinguish GitHub Free from a paid plan.
			var account struct {
				Login string
				Plan  struct{ Name string }
			}
			path := "/user"
			if r.Owner.Type == "Organization" {
				path = "/orgs/" + url.PathEscape(r.Owner.Login)
			}
			if err := s.GetOwnerDefaults(path, &account); err != nil {
				return entry{}, err
			}
			if !strings.EqualFold(account.Login, r.Owner.Login) || account.Plan.Name == "" {
				return blocked("cannot determine the owner's private wiki entitlement"), nil
			}
			want = account.Plan.Name != "free"
		}
		return projection("Wiki", "", "PATCH", object{"has_wiki": want}).inspect(s)
	})
}
func defaultSecurity(s *github.Snapshot, r repositoryContext) []job {
	isPublic := r.Visibility == "public"
	values := map[string]bool{
		"dependabot_alerts":               false,
		"dependabot_security_updates":     false,
		"secret_scanning":                 isPublic && r.Owner.Type == "User",
		"secret_scanning_push_protection": isPublic && r.Owner.Type == "User",
		"private_vulnerability_reporting": false,
	}
	if r.Owner.Type == "Organization" {
		var defaults []struct {
			Scope         string `json:"default_for_new_repos"`
			Configuration map[string]any
		}
		if err := s.GetOwnerDefaults("/orgs/"+url.PathEscape(r.Owner.Login)+"/code-security/configurations/defaults", &defaults); err != nil {
			return []job{unavailable("organization security defaults", err)}
		}
		for _, d := range defaults {
			if d.Scope != "all" && d.Scope != r.Visibility && !(d.Scope == "private_and_internal" && !isPublic) {
				continue
			}
			for key := range values {
				switch d.Configuration[key] {
				case "enabled":
					values[key] = true
				case "disabled":
					values[key] = false
				case "not_set": // Use the creation baseline for fields the configuration leaves unset.
				default:
					return []job{unavailable("organization security defaults", fmt.Errorf("unknown default for %s", key))}
				}
			}
		}
	}
	var p profile.Profile
	p.Security.DependabotAlerts = new(values["dependabot_alerts"])
	p.Security.DependabotUpdates = new(values["dependabot_security_updates"])
	// Unlicensed private repositories can expose the security block without these features.
	if isPublic || r.Security == nil || r.Security["secret_scanning"].Status != "" || values["secret_scanning"] {
		p.Security.SecretScanning = new(values["secret_scanning"])
	}
	if isPublic || r.Security == nil || r.Security["secret_scanning_push_protection"].Status != "" || values["secret_scanning_push_protection"] {
		p.Security.PushProtection = new(values["secret_scanning_push_protection"])
	}
	p.Security.PrivateReporting = new(values["private_vulnerability_reporting"])
	return settings(p)
}

func defaultActions(s *github.Snapshot, r repositoryContext) []job {
	var p profile.Profile
	a := &p.Actions
	if r.Owner.Type == "User" {
		a.DefaultPermissions = "read"
		a.Allowed = "all"
		a.Approval = "first_time_contributors"
		return settings(p)
	}
	var unresolved []job
	org := "/orgs/" + url.PathEscape(r.Owner.Login) + "/actions/permissions"
	var workflow struct {
		Default string `json:"default_workflow_permissions"`
	}
	if err := s.GetOwnerDefaults(org+"/workflow", &workflow); err != nil {
		unresolved = append(unresolved, unavailable("workflow token permissions", err))
	} else if workflow.Default == "read" || workflow.Default == "write" {
		a.DefaultPermissions = workflow.Default
	} else {
		unresolved = append(unresolved, unavailable("workflow token permissions", fmt.Errorf("unknown organization workflow permissions")))
	}
	var policy struct {
		Allowed string `json:"allowed_actions"`
		Enabled string `json:"enabled_repositories"`
	}
	if err := s.GetOwnerDefaults(org, &policy); err != nil {
		unresolved = append(unresolved, unavailable("allowed actions", err))
	} else {
		switch policy.Allowed {
		case "all", "local_only":
			a.Allowed = policy.Allowed
		case "selected":
			var list profile.Selected
			if err := s.GetOwnerDefaults(org+"/selected-actions", &list); err != nil {
				unresolved = append(unresolved, unavailable("selected actions", err))
			} else {
				a.Allowed = "selected"
				a.Selected = &list
			}
		default:
			if policy.Enabled != "none" {
				unresolved = append(unresolved, unavailable("allowed actions", fmt.Errorf("organization allowlist unavailable")))
			}
		}
	}
	if r.Visibility == "public" {
		var approval struct {
			Policy string `json:"approval_policy"`
		}
		if err := s.GetOwnerDefaults(org+"/fork-pr-contributor-approval", &approval); err != nil {
			unresolved = append(unresolved, unavailable("external contributor approval", err))
		} else {
			switch approval.Policy {
			case "all_external_contributors", "first_time_contributors", "first_time_contributors_new_to_github":
				a.Approval = approval.Policy
			default:
				unresolved = append(unresolved, unavailable("external contributor approval", fmt.Errorf("unknown organization approval policy")))
			}
		}
	}
	return append(settings(p), unresolved...)
}
