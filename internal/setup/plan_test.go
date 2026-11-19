package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
)

func server(t *testing.T, h http.HandlerFunc) *github.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &github.Client{HTTP: srv.Client(), Base: srv.URL, Repo: "owner/repo"}
}
func plan(t *testing.T, c *github.Client, p profile.Profile) Plan {
	t.Helper()
	v, err := Build(t.Context(), c, p)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestSoloConvergesPreservingStrongerRules(t *testing.T) {
	repo := object{"default_branch": "trunk", "allow_merge_commit": false, "allow_squash_merge": true, "delete_branch_on_merge": false, "has_issues": true, "security_and_analysis": object{"secret_scanning": object{"status": "enabled"}, "secret_scanning_push_protection": object{"status": "enabled"}}}
	var writes []string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /":
			json.NewEncoder(w).Encode(repo)
		case "GET /vulnerability-alerts":
			w.WriteHeader(204)
		case "GET /automated-security-fixes":
			fmt.Fprint(w, `{"enabled":true}`)
		case "GET /actions/permissions/workflow":
			fmt.Fprint(w, `{"default_workflow_permissions":"read"}`)
		case "GET /rules/branches/trunk":
			fmt.Fprint(w, `[{"type":"pull_request","parameters":{"required_approving_review_count":2}},{"type":"deletion"},{"type":"non_fast_forward"}]`)
		case "PATCH /":
			var m object
			json.NewDecoder(r.Body).Decode(&m)
			for k, v := range m {
				writes = append(writes, k)
				repo[k] = v
			}
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	p, _ := profile.Load("solo")
	before := plan(t, c, p)
	if before.Exit(true) != 2 || len(writes) != 0 {
		t.Fatal("unsafe or inaccurate dry run")
	}
	if got := before.Apply(t.Context(), c); got.Exit(true) != 0 {
		t.Fatal(got.items)
	}
	if got := plan(t, c, p).Apply(t.Context(), c); got.Exit(true) != 0 || !slices.Equal(writes, []string{"allow_merge_commit", "delete_branch_on_merge"}) {
		t.Fatal(writes, got.items)
	}
}
func TestIndependentWritesSurvivePolicyDenial(t *testing.T) {
	var p profile.Profile
	yes := true
	p.Repository.Issues = &yes
	p.Repository.Discussions = &yes
	issues := false
	var attempts []string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(object{"has_issues": issues, "has_discussions": false})
			return
		}
		var m object
		json.NewDecoder(r.Body).Decode(&m)
		if _, ok := m["has_issues"]; ok {
			issues = true
			attempts = append(attempts, "issues")
			return
		}
		attempts = append(attempts, "discussions")
		http.Error(w, `{"message":"policy denied"}`, 403)
	})
	for range 2 {
		result := plan(t, c, p).Apply(t.Context(), c)
		if result.Exit(false) != 1 {
			t.Fatal(result.items)
		}
	}
	if !slices.Equal(attempts, []string{"issues", "discussions", "discussions"}) {
		t.Fatal(attempts)
	}
}
func TestHumanEditBetweenPlanAndApply(t *testing.T) {
	var p profile.Profile
	p.Repository.MergeMethods = []string{"merge"}
	state := object{"allow_merge_commit": false, "allow_squash_merge": true, "allow_rebase_merge": true}
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("stale write")
		}
		json.NewEncoder(w).Encode(state)
	})
	initial := plan(t, c, p)
	state["allow_squash_merge"] = false
	if initial.Apply(t.Context(), c).Exit(false) != 1 {
		t.Fatal("expected conflict")
	}
}
func TestFilePRRecoversAndConverges(t *testing.T) {
	var p profile.Profile
	p.Files.PRTemplate = true
	var writes []string
	branch := ""
	prOpen := false
	merged := false
	denyPR := true
	content, _ := templates.ReadFile("templates/pull-request.md")
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes = append(writes, r.URL.Path)
		}
		switch {
		case r.URL.Path == "/":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.URL.Path == "/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"base"}}`)
		case r.URL.Path == "/git/commits/base":
			fmt.Fprint(w, `{"tree":{"sha":"root"}}`)
		case r.URL.Path == "/git/trees/root":
			if merged {
				fmt.Fprint(w, `{"tree":[{"path":"PULL_REQUEST_TEMPLATE.md","type":"blob","sha":"custom"}]}`)
			} else {
				fmt.Fprint(w, `{"tree":[]}`)
			}
		case r.URL.Path == "/pulls" && r.Method == "GET":
			if prOpen {
				fmt.Fprint(w, `[{"state":"open","html_url":"https://github.com/owner/repo/pull/1","head":{"sha":"tip"}}]`)
			} else {
				fmt.Fprint(w, "[]")
			}
		case strings.HasPrefix(r.URL.Path, "/git/ref/heads/chore/"):
			if branch == "" {
				http.NotFound(w, r)
			} else {
				fmt.Fprint(w, `{"object":{"sha":"tip"}}`)
			}
		case r.URL.Path == "/git/trees":
			var req struct {
				Base string `json:"base_tree"`
				Tree []struct{ Path, Content string }
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Base != "root" || len(req.Tree) != 1 || req.Tree[0].Path != ".github/pull_request_template.md" || req.Tree[0].Content != string(content) {
				t.Error(req)
			}
			fmt.Fprint(w, `{"sha":"newtree"}`)
		case r.URL.Path == "/git/commits":
			fmt.Fprint(w, `{"sha":"tip"}`)
		case r.URL.Path == "/git/refs":
			branch = "created"
		case r.URL.Path == "/compare/base...tip":
			fmt.Fprintf(w, `{"ahead_by":1,"files":[{"filename":".github/pull_request_template.md","status":"added","sha":"%s"}]}`, blobID(string(content)))
		case r.URL.Path == "/pulls" && r.Method == "POST":
			if denyPR {
				http.Error(w, `{"message":"PR creation denied"}`, 403)
			} else {
				prOpen = true
				fmt.Fprint(w, "{}")
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	first := plan(t, c, p)
	var preview bytes.Buffer
	first.Print(&preview)
	if !strings.Contains(preview.String(), "+ ## What changed?") || len(writes) > 0 {
		t.Fatal(preview.String())
	}
	if first.Apply(t.Context(), c).Exit(false) != 1 {
		t.Fatal("expected interrupted PR creation")
	}
	denyPR = false
	for range 2 {
		result := plan(t, c, p).Apply(t.Context(), c)
		if result.Exit(false) != 0 || result.Exit(true) != 2 {
			t.Fatal(result.items)
		}
	}
	merged = true
	if plan(t, c, p).Exit(true) != 0 {
		t.Fatal("missing merged template")
	}
	if !slices.Equal(writes, []string{"/git/trees", "/git/commits", "/git/refs", "/pulls", "/pulls"}) {
		t.Fatal(writes)
	}
}

func TestSelectedActionsResumeAfterPolicyChange(t *testing.T) {
	var p profile.Profile
	p.Actions.Allowed = "selected"
	p.Actions.Selected = &profile.Selected{GithubOwned: true, Patterns: []string{"org/tool@*"}}
	mode := "all"
	selected := profile.Selected{}
	var writes []string
	denied := true
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, "{}")
		case "/actions/permissions":
			if r.Method == "PUT" {
				var body object
				json.NewDecoder(r.Body).Decode(&body)
				if body["enabled"] != true {
					t.Error("lost enabled state")
				}
				mode = "selected"
				writes = append(writes, "mode")
				return
			}
			json.NewEncoder(w).Encode(object{"enabled": true, "allowed_actions": mode})
		case "/actions/permissions/selected-actions":
			if mode != "selected" {
				t.Error("accessed allowlist too early")
			}
			if r.Method == "PUT" {
				writes = append(writes, "list")
				if denied {
					http.Error(w, `{"message":"policy"}`, 403)
					return
				}
				json.NewDecoder(r.Body).Decode(&selected)
				return
			}
			json.NewEncoder(w).Encode(selected)
		default:
			http.NotFound(w, r)
		}
	})
	first := plan(t, c, p)
	if len(writes) != 0 || first.Exit(true) != 2 {
		t.Fatal(first.items)
	}
	if first.Apply(t.Context(), c).Exit(false) != 1 {
		t.Fatal("expected partial apply")
	}
	denied = false
	for range 2 {
		if result := plan(t, c, p).Apply(t.Context(), c); result.Exit(true) != 0 {
			t.Fatal(result.items)
		}
	}
	if !slices.Equal(writes, []string{"mode", "list", "list"}) {
		t.Fatal(writes)
	}
}

func TestBranchCreatesOnlyMissingRules(t *testing.T) {
	p := profile.Profile{}
	one := 1
	yes := true
	p.Branch = &profile.Branch{Target: "default", RequirePR: &yes, Approvals: &one, BlockDeletion: &yes, BlockForce: &yes}
	active := []rule{{Type: "pull_request", Parameters: object{"required_approving_review_count": 2}}}
	writes := 0
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `{"default_branch":"trunk"}`)
		case "/rules/branches/trunk":
			json.NewEncoder(w).Encode(active)
		case "/branches/trunk/protection":
			http.NotFound(w, r)
		case "/rulesets":
			if r.Method == "GET" {
				fmt.Fprint(w, "[]")
				return
			}
			var body struct {
				Rules      []rule
				Conditions object
			}
			json.NewDecoder(r.Body).Decode(&body)
			if len(body.Rules) != 2 || body.Rules[0].Type != "non_fast_forward" || body.Rules[1].Type != "deletion" || !strings.Contains(encode(body.Conditions), "~DEFAULT_BRANCH") {
				t.Error(body)
			}
			active = append(active, body.Rules...)
			writes++
			w.WriteHeader(201)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	})
	for range 2 {
		if res := plan(t, c, p).Apply(t.Context(), c); res.Exit(true) != 0 {
			t.Fatal(res.items)
		}
	}
	if writes != 1 {
		t.Fatal(writes)
	}
}
