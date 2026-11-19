package setup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
)

func defaultProfile(t *testing.T) profile.Profile {
	t.Helper()
	p, err := profile.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultRestoresSoloAndConverges(t *testing.T) {
	var writes []string
	alerts, updates, managed := true, true, true
	repo := object{"owner": object{"type": "User", "login": "owner"}, "visibility": "public", "allow_merge_commit": true, "allow_rebase_merge": true, "allow_squash_merge": true, "delete_branch_on_merge": true, "has_wiki": true, "has_issues": true, "has_discussions": false, "permissions": object{"admin": true}, "security_and_analysis": object{"secret_scanning": object{"status": "enabled"}, "secret_scanning_push_protection": object{"status": "enabled"}}}
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes = append(writes, r.Method+" "+r.URL.Path)
		}
		switch r.URL.Path {
		case "/":
			if r.Method == "PATCH" {
				var body object
				json.NewDecoder(r.Body).Decode(&body)
				if encode(body) != `{"delete_branch_on_merge":false}` {
					t.Errorf("unexpected patch %v", body)
				}
				repo["delete_branch_on_merge"] = false
			}
			json.NewEncoder(w).Encode(repo)
		case "/automated-security-fixes":
			if r.Method == "DELETE" {
				updates = false
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(object{"enabled": updates})
		case "/vulnerability-alerts":
			if r.Method == "DELETE" {
				if updates {
					t.Error("disabled alerts before updates")
				}
				alerts = false
				w.WriteHeader(204)
				return
			}
			if alerts {
				w.WriteHeader(204)
			} else {
				http.NotFound(w, r)
			}
		case "/private-vulnerability-reporting":
			fmt.Fprint(w, `{"enabled":false}`)
		case "/actions/permissions":
			fmt.Fprint(w, `{"enabled":true,"allowed_actions":"all"}`)
		case "/actions/permissions/workflow":
			fmt.Fprint(w, `{"default_workflow_permissions":"read"}`)
		case "/actions/permissions/fork-pr-contributor-approval":
			fmt.Fprint(w, `{"approval_policy":"first_time_contributors"}`)
		case "/rulesets":
			if r.URL.Query().Get("includes_parents") != "false" {
				t.Error("included parent rulesets")
			}
			sets := []object{{"id": 1, "name": "human policy"}}
			if managed {
				sets = append(sets, object{"id": 9, "name": rulesetName})
			}
			json.NewEncoder(w).Encode(sets)
		case "/rulesets/9":
			if r.Method == "DELETE" {
				managed = false
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(object{"source": "owner/repo", "source_type": "Repository", "target": "branch", "name": rulesetName, "enforcement": "active", "conditions": defaultBranchCondition(), "bypass_actors": []object{}, "rules": []rule{{Type: "deletion"}, {Type: "non_fast_forward"}}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	p := defaultProfile(t)
	before := plan(t, c, p)
	if before.Exit(true) != 2 || len(writes) != 0 {
		t.Fatal(before.items, writes)
	}
	if result := before.Apply(t.Context(), c); result.Exit(true) != 0 {
		t.Fatal(result.items)
	}
	if result := plan(t, c, p).Apply(t.Context(), c); result.Exit(true) != 0 {
		t.Fatal(result.items)
	}
	if !slices.Equal(writes, []string{"PATCH /", "DELETE /automated-security-fixes", "DELETE /vulnerability-alerts", "DELETE /rulesets/9"}) {
		t.Fatal(writes)
	}
}
func TestDefaultPreservesCustomizedRuleset(t *testing.T) {
	for _, custom := range []string{`{"type":"required_signatures"}`, `{"type":"pull_request","parameters":{"required_approving_review_count":2,"require_code_owner_review":true}}`} {
		c := server(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Error("unexpected write")
			}
			if r.URL.Path == "/rulesets" {
				fmt.Fprint(w, `[{"id":10,"name":"gh-tune: default branch"}]`)
				return
			}
			fmt.Fprintf(w, `{"name":"gh-tune: default branch","source_type":"Repository","source":"owner/repo","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"bypass_actors":[],"rules":[%s]}`, custom)
		})
		e := inspect(removeManagedRuleset(c.Repo), c.Snapshot(t.Context()))
		if e.state != "BLOCKED" {
			t.Fatal(e)
		}
	}
}
func TestDefaultOrganizationPolicies(t *testing.T) {
	r := repositoryContext{Visibility: "public"}
	r.Owner.Type = "Organization"
	r.Owner.Login = "owner"
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected write")
		}
		switch r.URL.Path {
		case "/orgs/owner/code-security/configurations/defaults":
			fmt.Fprint(w, `[{"default_for_new_repos":"public","configuration":{"dependabot_alerts":"enabled","dependabot_security_updates":"enabled","secret_scanning":"enabled","secret_scanning_push_protection":"enabled","private_vulnerability_reporting":"disabled"}}]`)
		case "/orgs/owner/actions/permissions":
			fmt.Fprint(w, `{"allowed_actions":"local_only"}`)
		case "/orgs/owner/actions/permissions/workflow":
			fmt.Fprint(w, `{"default_workflow_permissions":"write"}`)
		case "/orgs/owner/actions/permissions/fork-pr-contributor-approval":
			w.WriteHeader(403)
		case "/actions/permissions":
			fmt.Fprint(w, `{"enabled":true,"allowed_actions":"local_only"}`)
		case "/actions/permissions/workflow":
			fmt.Fprint(w, `{"default_workflow_permissions":"write"}`)
		case "/vulnerability-alerts":
			w.WriteHeader(204)
		case "/automated-security-fixes":
			fmt.Fprint(w, `{"enabled":true}`)
		case "/private-vulnerability-reporting":
			fmt.Fprint(w, `{"enabled":false}`)
		case "/":
			fmt.Fprint(w, `{"visibility":"public","security_and_analysis":{"secret_scanning":{"status":"enabled"},"secret_scanning_push_protection":{"status":"enabled"}}}`)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	})
	s := c.Snapshot(t.Context())
	for _, j := range append(defaultSecurity(s, r), defaultActions(s, r)...) {
		e := inspect(j, s)
		if j.name == "external contributor approval" {
			if e.state != "BLOCKED" || !strings.Contains(e.detail, "403") {
				t.Fatal(e)
			}
		} else if e.state != "MATCH" {
			t.Fatal(j.name, e)
		}
	}
}

func TestPrivatePersonalCreationDefaults(t *testing.T) {
	r := repositoryContext{Visibility: "private", Security: map[string]struct{ Status string }{}}
	r.Owner.Type = "User"
	r.Owner.Login = "owner"
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"login":"owner","plan":{"name":"free"}}`)
		case "/":
			fmt.Fprint(w, `{"has_wiki":false,"visibility":"private"}`)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	})
	s := c.Snapshot(t.Context())
	if e := inspect(defaultWiki(r), s); e.state != "MATCH" {
		t.Fatal(e)
	}
	for _, j := range defaultSecurity(s, r) {
		if j.name == "Secret scanning" || j.name == "Push protection" {
			t.Fatal("unexpected licensed feature", j.name)
		}
		if j.name == "private vulnerability reporting" && inspect(j, s).state != "N/A" {
			t.Fatal("private reporting")
		}
	}
}
