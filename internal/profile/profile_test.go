package profile

import "testing"

func TestProfiles(t *testing.T) {
	for _, name := range []string{"team", "solo", "opensource"} {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if !*p.Repository.EnableMergeCommit || p.Repository.MergeMethods != nil {
			t.Fatal("built-in merge policy")
		}
		if name == "solo" && (p.Branch.RequirePR != nil || p.Files.PRTemplate || p.Files.IssueTemplate || len(p.Files.Dependabot) > 0) {
			t.Fatal("unexpected solo requirement")
		}
	}
	for _, body := range []string{"version: 2", "version: 1\nother: false", "version: 1\nrepository: {wiki: false, wiki: true}", "version: 1\nrepository: {merge_methods: []}", "version: 1\nbranch: {target: default, require_pull_request: false, approvals: 1}", "version: 1\nactions: {allowed_actions: selected}", "version: 1\n---\nversion: 1"} {
		if _, err := parse([]byte(body)); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
}
