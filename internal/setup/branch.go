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
	var id int
	for page := 1; ; page++ {
		var sets []struct {
			ID   int
			Name string
		}
		if err := s.Get(fmt.Sprintf("/rulesets?includes_parents=false&per_page=100&page=%d", page), &sets); err != nil {
			return entry{}, err
		}
		for _, rs := range sets {
			if rs.Name == rulesetName {
				if id != 0 {
					return blocked("multiple rulesets named " + rulesetName), nil
				}
				id = rs.ID
			}
		}
		if len(sets) < 100 {
			break
		}
	}
	conditions := object{"ref_name": object{"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{}}}
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
