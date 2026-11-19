package setup

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
)

const rulesetName = "gh-tune: default branch"

type rule struct {
	Type       string `json:"type"`
	Parameters object `json:"parameters,omitempty"`
}
type minimum struct {
	pr, force, deletion bool
	approvals           int
}

func enabled(v *bool) bool { return v != nil && *v }
func (m *minimum) add(r rule) {
	switch r.Type {
	case "deletion":
		m.deletion = true
	case "non_fast_forward":
		m.force = true
	case "pull_request":
		m.pr = true
		if n, ok := r.Parameters["required_approving_review_count"].(float64); ok {
			m.approvals = max(m.approvals, int(n))
		}
	}
}
func (m minimum) missing(w minimum) []rule {
	var res []rule
	if w.force && !m.force {
		res = append(res, rule{Type: "non_fast_forward"})
	}
	if w.deletion && !m.deletion {
		res = append(res, rule{Type: "deletion"})
	}
	if w.pr && (!m.pr || m.approvals < w.approvals) {
		res = append(res, rule{Type: "pull_request", Parameters: object{"required_approving_review_count": w.approvals, "require_code_owner_review": false, "dismiss_stale_reviews_on_push": false, "require_last_push_approval": false, "required_review_thread_resolution": false}})
	}
	return res
}
func branchJob(b profile.Branch) job {
	want := minimum{pr: enabled(b.RequirePR), force: enabled(b.BlockForce), deletion: enabled(b.BlockDeletion)}
	if b.Approvals != nil {
		want.approvals = *b.Approvals
	}
	return setting("default branch protection", func(s *github.Snapshot) (entry, error) {
		if want == (minimum{}) {
			return match("no minimum requested"), nil
		}
		var repo struct {
			Default string `json:"default_branch"`
		}
		if err := s.Get("", &repo); err != nil {
			return entry{}, err
		}
		if repo.Default == "" {
			return blocked("no default branch"), nil
		}
		branch := url.PathEscape(repo.Default)
		var active []rule
		for page := 1; ; page++ {
			var part []rule
			if err := s.Get(fmt.Sprintf("/rules/branches/%s?per_page=100&page=%d", branch, page), &part); err != nil {
				return entry{}, err
			}
			active = append(active, part...)
			if len(part) < 100 {
				break
			}
		}
		var actual minimum
		for _, r := range active {
			actual.add(r)
		}
		if len(actual.missing(want)) == 0 {
			return match("active rules meet the minimum"), nil
		}
		var classic struct {
			Reviews *struct {
				Approvals int `json:"required_approving_review_count"`
			} `json:"required_pull_request_reviews"`
			Force    *struct{ Enabled bool } `json:"allow_force_pushes"`
			Deletion *struct{ Enabled bool } `json:"allow_deletions"`
		}
		err := s.Get("/branches/"+branch+"/protection", &classic)
		if err == nil {
			if classic.Reviews != nil {
				actual.pr = true
				actual.approvals = max(actual.approvals, classic.Reviews.Approvals)
			}
			actual.force = actual.force || (classic.Force != nil && !classic.Force.Enabled)
			actual.deletion = actual.deletion || (classic.Deletion != nil && !classic.Deletion.Enabled)
		} else if !github.Status(err, 404) {
			return entry{}, err
		}
		missing := actual.missing(want)
		if len(missing) == 0 {
			return match("existing protection meets the minimum"), nil
		}
		return planRuleset(s, missing)
	})
}
func planRuleset(s *github.Snapshot, missing []rule) (entry, error) {
	id, err := managedRulesetID(s)
	if err != nil {
		return entry{}, err
	}
	conditions := defaultBranchCondition()
	labels := []string{}
	for _, r := range missing {
		label := r.Type
		if r.Type == "pull_request" {
			label += fmt.Sprintf(" (%v approvals)", r.Parameters["required_approving_review_count"])
		}
		labels = append(labels, label)
	}
	path := "/rulesets"
	method := "POST"
	rules := missing
	if id != 0 {
		path = fmt.Sprintf("%s/%d", path, id)
		method = "PUT"
		var existing struct {
			Target, Enforcement string
			Conditions          object
			Bypass              *[]object `json:"bypass_actors"`
			Rules               []rule
		}
		if err := s.Get(path, &existing); err != nil {
			return entry{}, err
		}
		if existing.Target != "branch" || existing.Enforcement != "active" || encode(existing.Conditions) != encode(conditions) || existing.Bypass == nil || len(*existing.Bypass) > 0 {
			return blocked("reserved ruleset has unexpected settings or hidden bypass actors; review it on GitHub"), nil
		}
		rules = slices.Clone(existing.Rules)
		for _, r := range rules {
			if !slices.Contains([]string{"pull_request", "non_fast_forward", "deletion"}, r.Type) {
				return blocked("reserved ruleset has additional rules; review it on GitHub"), nil
			}
		}
		for _, r := range missing {
			i := slices.IndexFunc(rules, func(old rule) bool { return old.Type == r.Type })
			if i < 0 {
				rules = append(rules, r)
			} else if r.Type == "pull_request" {
				if rules[i].Parameters == nil {
					return blocked("missing review parameters"), nil
				}
				rules[i].Parameters["required_approving_review_count"] = r.Parameters["required_approving_review_count"]
			}
		}
	}
	body := object{"name": rulesetName, "target": "branch", "enforcement": "active", "conditions": conditions, "rules": rules}
	return change("require "+strings.Join(labels, ", "), method, path, body), nil
}

func managedRulesetID(s *github.Snapshot) (int, error) {
	var id int
	for page := 1; ; page++ {
		var sets []struct {
			ID   int
			Name string
		}
		if err := s.Get(fmt.Sprintf("/rulesets?includes_parents=false&per_page=100&page=%d", page), &sets); err != nil {
			return 0, err
		}
		for _, rs := range sets {
			if rs.Name == rulesetName {
				if id != 0 {
					return 0, fmt.Errorf("multiple rulesets named %s", rulesetName)
				}
				id = rs.ID
			}
		}
		if len(sets) < 100 {
			break
		}
	}
	return id, nil
}
func defaultBranchCondition() object {
	return object{"ref_name": object{"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{}}}
}

func removeManagedRuleset(repo string) job {
	return setting("gh-tune branch ruleset", func(s *github.Snapshot) (entry, error) {
		id, err := managedRulesetID(s)
		if err != nil {
			return entry{}, err
		}
		if id == 0 {
			return match("no gh-tune ruleset"), nil
		}
		path := fmt.Sprintf("/rulesets/%d", id)
		var rs struct {
			Name, Target, Enforcement, Source string
			SourceType                        string `json:"source_type"`
			Conditions                        object
			Bypass                            *[]object `json:"bypass_actors"`
			Rules                             []rule
		}
		if err = s.Get(path, &rs); err != nil {
			return entry{}, err
		}
		if rs.SourceType != "Repository" || !strings.EqualFold(rs.Source, repo) || rs.Name != rulesetName || rs.Target != "branch" || rs.Enforcement != "active" || encode(rs.Conditions) != encode(defaultBranchCondition()) || rs.Bypass == nil || len(*rs.Bypass) != 0 || len(rs.Rules) == 0 {
			return blocked("reserved ruleset has customized or unreadable settings; review it on GitHub"), nil
		}
		var labels []string
		for _, r := range rs.Rules {
			if !generatedRule(r) {
				return blocked("reserved ruleset contains custom protection; review it on GitHub"), nil
			}
			label := r.Type
			if r.Type == "pull_request" {
				label += fmt.Sprintf(" (%v approvals)", r.Parameters["required_approving_review_count"])
			}
			labels = append(labels, label)
		}
		e := change(fmt.Sprintf("remove %q (#%d): %s", rulesetName, id, strings.Join(labels, ", ")), "DELETE", path, nil)
		e.guard += encode(rs.Rules)
		return e, nil
	})
}

// Only remove rules the extension can create, including GitHub's normalized defaults.
func generatedRule(r rule) bool {
	switch r.Type {
	case "deletion", "non_fast_forward":
		return len(r.Parameters) == 0
	case "pull_request":
		if n, ok := r.Parameters["required_approving_review_count"].(float64); !ok || n < 0 || n > 10 || n != float64(int(n)) {
			return false
		}
		for k, v := range r.Parameters {
			switch k {
			case "required_approving_review_count":
			case "require_code_owner_review", "dismiss_stale_reviews_on_push", "require_last_push_approval", "required_review_thread_resolution", "require_extra_approval_for_unattributed_changes":
				if v != false {
					return false
				}
			case "allowed_merge_methods":
				var methods []string
				raw, ok := v.([]any)
				if !ok {
					return false
				}
				for _, m := range raw {
					name, ok := m.(string)
					if !ok {
						return false
					}
					methods = append(methods, name)
				}
				slices.Sort(methods)
				if !slices.Equal(methods, []string{"merge", "rebase", "squash"}) {
					return false
				}
			case "dismissal_restriction":
				if encode(v) != encode(object{"enabled": false, "allowed_actors": []string{}}) {
					return false
				}
			case "required_reviewers":
				if encode(v) != "[]" {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}
