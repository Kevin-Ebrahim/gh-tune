// Package profile loads versioned, strictly validated repository policies.
package profile

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed profiles/*.yaml
var profiles embed.FS

type Profile struct {
	Version    int
	Baseline   string
	Repository struct {
		EnableMergeCommit         *bool    `yaml:"enable_merge_commit"`
		MergeMethods              []string `yaml:"merge_methods"`
		DeleteBranchOnMerge       *bool    `yaml:"delete_branch_on_merge"`
		Issues, Wiki, Discussions *bool
		PullRequests              *bool  `yaml:"pull_requests"`
		IssueCreationPolicy       string `yaml:"issue_creation_policy"`
		PullRequestCreationPolicy string `yaml:"pull_request_creation_policy"`
	}
	Security struct {
		DependabotAlerts  *bool `yaml:"dependabot_alerts"`
		DependabotUpdates *bool `yaml:"dependabot_security_updates"`
		SecretScanning    *bool `yaml:"secret_scanning"`
		PushProtection    *bool `yaml:"push_protection"`
		PrivateReporting  *bool `yaml:"private_vulnerability_reporting"`
	}
	Branch  *Branch
	Actions struct {
		DefaultPermissions string    `yaml:"default_workflow_permissions"`
		Allowed            string    `yaml:"allowed_actions"`
		Selected           *Selected `yaml:"selected_actions"`
		Approval           string    `yaml:"external_contributor_approval"`
	}
	Files struct {
		IssueTemplate bool `yaml:"issue_template"`
		PRTemplate    bool `yaml:"pull_request_template"`
		Dependabot    []Ecosystem
	}
}
type Branch struct {
	Target        string
	RequirePR     *bool `yaml:"require_pull_request"`
	Approvals     *int
	BlockForce    *bool `yaml:"block_force_pushes"`
	BlockDeletion *bool `yaml:"block_deletion"`
}
type Selected struct {
	GithubOwned bool     `yaml:"github_owned" json:"github_owned_allowed"`
	Verified    bool     `json:"verified_allowed"`
	Patterns    []string `json:"patterns_allowed"`
}
type Ecosystem struct {
	Ecosystem string `yaml:"package-ecosystem"`
	Directory string
}

func Load(name string) (Profile, error) {
	var b []byte
	var err error
	switch name {
	case "default", "solo", "team", "opensource":
		b, err = profiles.ReadFile("profiles/" + name + ".yaml")
	default:
		b, err = os.ReadFile(name)
	}
	if err != nil {
		return Profile{}, err
	}
	return parse(b)
}
func parse(b []byte) (Profile, error) {
	var p Profile
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, fmt.Errorf("expected one YAML document")
	}
	return p, p.validate()
}
func (p Profile) validate() error {
	if p.Version != 1 {
		return fmt.Errorf("expected profile version 1")
	}
	if p.Baseline != "" && (p.Baseline != "github" || !reflect.DeepEqual(p, Profile{Version: 1, Baseline: "github"})) {
		return fmt.Errorf("baseline: github is a standalone profile")
	}
	r := p.Repository
	if r.MergeMethods != nil {
		if r.EnableMergeCommit != nil || len(r.MergeMethods) == 0 {
			return fmt.Errorf("merge_methods requires a nonempty exact list and excludes enable_merge_commit")
		}
		seen := map[string]bool{}
		for _, v := range r.MergeMethods {
			if !oneOf(v, "merge", "squash", "rebase") || seen[v] {
				return fmt.Errorf("invalid or duplicate merge method %q", v)
			}
			seen[v] = true
		}
	}
	if r.EnableMergeCommit != nil && !*r.EnableMergeCommit {
		return fmt.Errorf("use merge_methods to disable a merge method")
	}
	if !oneOf(r.IssueCreationPolicy, "", "all", "collaborators_only") ||
		!oneOf(r.PullRequestCreationPolicy, "", "all", "collaborators_only") {
		return fmt.Errorf("creation policies must be all or collaborators_only")
	}
	b := p.Branch
	if b != nil {
		if b.Target != "default" {
			return fmt.Errorf("branch.target must be default")
		}
		if b.Approvals != nil && (*b.Approvals < 0 || *b.Approvals > 10 || b.RequirePR == nil || !*b.RequirePR) {
			return fmt.Errorf("approvals requires require_pull_request and a count from 0 to 10")
		}
	}
	s := p.Security
	if contradicts(s.DependabotUpdates, s.DependabotAlerts) || contradicts(s.PushProtection, s.SecretScanning) {
		return fmt.Errorf("enabled security feature has a disabled prerequisite")
	}
	a := p.Actions
	if !oneOf(a.DefaultPermissions, "", "read", "write") || !oneOf(a.Allowed, "", "all", "local_only", "selected") || !oneOf(a.Approval, "", "all_external_contributors", "first_time_contributors", "first_time_contributors_new_to_github") {
		return fmt.Errorf("invalid Actions policy")
	}
	if (a.Selected != nil) != (a.Allowed == "selected") {
		return fmt.Errorf("selected_actions and allowed_actions: selected must be specified together")
	}
	if a.Selected != nil {
		for _, v := range a.Selected.Patterns {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("empty selected action pattern")
			}
		}
	}
	seen := map[Ecosystem]bool{}
	for _, e := range p.Files.Dependabot {
		if !oneOf(e.Ecosystem, "gomod", "github-actions", "npm", "pip", "cargo", "docker", "bundler", "maven", "gradle", "nuget", "composer", "pub", "swift", "terraform") || !strings.HasPrefix(e.Directory, "/") || path.Clean(e.Directory) != e.Directory || seen[e] {
			return fmt.Errorf("invalid or duplicate Dependabot entry: %v", e)
		}
		seen[e] = true
	}
	return nil
}
func oneOf(v string, values ...string) bool {
	for _, w := range values {
		if v == w {
			return true
		}
	}
	return false
}
func contradicts(child, parent *bool) bool {
	return child != nil && *child && parent != nil && !*parent
}
