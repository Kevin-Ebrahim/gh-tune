package setup

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
	"go.yaml.in/yaml/v3"
)

//go:embed templates/*.md
var templates embed.FS

type treeEntry struct{ Path, Type, SHA string }
type fileTree struct {
	s    *github.Snapshot
	root string
}

func (t fileTree) list(sha string) ([]treeEntry, error) {
	var tree struct {
		Tree      []treeEntry
		Truncated bool
	}
	if err := t.s.Get("/git/trees/"+sha, &tree); err != nil {
		return nil, err
	}
	if tree.Truncated {
		return nil, fmt.Errorf("GitHub returned a truncated file tree")
	}
	return tree.Tree, nil
}
func (t fileTree) directory(dir string) ([]treeEntry, error) {
	sha := t.root
	if dir != "" {
		for _, name := range strings.Split(dir, "/") {
			entries, err := t.list(sha)
			if err != nil {
				return nil, err
			}
			i := slices.IndexFunc(entries, func(e treeEntry) bool { return e.Path == name })
			if i < 0 {
				return nil, nil
			}
			if entries[i].Type != "tree" {
				return nil, fmt.Errorf("%s is not a directory", name)
			}
			sha = entries[i].SHA
		}
	}
	return t.list(sha)
}
func (t fileTree) hasTemplate(kind string) (bool, error) {
	for _, dir := range []string{".github", "", "docs"} {
		entries, err := t.directory(dir)
		if err != nil {
			return false, err
		}
		for _, e := range entries {
			name := strings.ToLower(e.Path)
			if e.Type == "blob" && (name == kind || name == kind+".md" || name == kind+".txt") {
				return true, nil
			}
			if e.Type == "tree" && name == kind {
				children, err := t.list(e.SHA)
				if err != nil {
					return false, err
				}
				for _, child := range children {
					n := strings.ToLower(child.Path)
					ext := path.Ext(n)
					if child.Type == "blob" && (ext == ".md" || ext == ".txt" || kind == "issue_template" && (ext == ".yml" || ext == ".yaml") && n != "config.yml" && n != "config.yaml") {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

type update struct {
	Ecosystem   string   `yaml:"package-ecosystem"`
	Directory   string   `yaml:",omitempty"`
	Directories []string `yaml:",omitempty"`
	Schedule    struct{ Interval string }
}
type dependabot struct {
	Version int
	Updates []update
}

func (t fileTree) dependabot(want []profile.Ecosystem) (string, error) {
	files, err := t.directory(".github")
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.Path != "dependabot.yml" && f.Path != "dependabot.yaml" {
			continue
		}
		var blob struct{ Content string }
		if err := t.s.Get("/git/blobs/"+f.SHA, &blob); err != nil {
			return "", err
		}
		data, err := base64.StdEncoding.DecodeString(blob.Content)
		if err != nil {
			return "", err
		}
		var d dependabot
		if err = yaml.Unmarshal(data, &d); err != nil {
			return "", err
		}
		for _, e := range want {
			if !slices.ContainsFunc(d.Updates, func(u update) bool {
				return d.Version == 2 && u.Ecosystem == e.Ecosystem && (u.Directory == e.Directory || slices.Contains(u.Directories, e.Directory)) && u.Schedule.Interval == "weekly"
			}) {
				return "", fmt.Errorf("existing .github/%s needs review for weekly %s updates in %s", f.Path, e.Ecosystem, e.Directory)
			}
		}
		return "", nil
	}
	d := dependabot{Version: 2}
	for _, e := range want {
		u := update{Ecosystem: e.Ecosystem, Directory: e.Directory}
		u.Schedule.Interval = "weekly"
		d.Updates = append(d.Updates, u)
	}
	raw, err := yaml.Marshal(d)
	return string(raw), err
}

type ref struct{ Object struct{ SHA string } }
type commit struct{ Tree struct{ SHA string } }
type pull struct {
	State  string
	URL    string  `json:"html_url"`
	Merged *string `json:"merged_at"`
	Head   struct{ SHA string }
}

func filesJob(p profile.Profile, repo string) job {
	return setting("repository files", func(s *github.Snapshot) (entry, error) {
		var metadata struct {
			Default string `json:"default_branch"`
		}
		if err := s.Get("", &metadata); err != nil {
			return entry{}, err
		}
		base := metadata.Default
		var head ref
		if err := s.Get("/git/ref/heads/"+url.PathEscape(base), &head); err != nil {
			if github.Status(err, 409) {
				return blocked("add the first commit before opening a setup PR"), nil
			}
			return entry{}, err
		}
		var parent commit
		if err := s.Get("/git/commits/"+head.Object.SHA, &parent); err != nil {
			return entry{}, err
		}
		t := fileTree{s, parent.Tree.SHA}
		files := map[string]string{}
		for _, v := range []struct {
			enabled       bool
			kind, to, src string
		}{{p.Files.IssueTemplate, "issue_template", ".github/ISSUE_TEMPLATE/bug-report.md", "issue.md"}, {p.Files.PRTemplate, "pull_request_template", ".github/pull_request_template.md", "pull-request.md"}} {
			if !v.enabled {
				continue
			}
			exists, err := t.hasTemplate(v.kind)
			if err != nil {
				return entry{}, err
			}
			if !exists {
				b, err := templates.ReadFile("templates/" + v.src)
				if err != nil {
					return entry{}, err
				}
				files[v.to] = string(b)
			}
		}
		if len(p.Files.Dependabot) > 0 {
			content, err := t.dependabot(p.Files.Dependabot)
			if err != nil {
				return entry{}, err
			}
			if content != "" {
				files[".github/dependabot.yml"] = content
			}
		}
		if len(files) == 0 {
			return match("requested files already present"), nil
		}
		return fileProposal(s, repo, base, head.Object.SHA, parent.Tree.SHA, files)
	})
}
func fileProposal(s *github.Snapshot, repo, base, parent, tree string, files map[string]string) (entry, error) {
	hash := sha256.Sum256([]byte(base + "\n" + encode(files)))
	branch := fmt.Sprintf("chore/gh-tune-%x", hash[:6])
	v := url.Values{"head": {strings.Split(repo, "/")[0] + ":" + branch}, "base": {base}, "state": {"all"}}
	var prs []pull
	if err := s.Get("/pulls?"+v.Encode(), &prs); err != nil {
		return entry{}, err
	}
	var head ref
	err := s.Get("/git/ref/heads/"+branch, &head)
	exists := err == nil
	if err != nil && !github.Status(err, 404) {
		return entry{}, err
	}
	if exists {
		// Recover only a branch whose entire diff contains exactly our absent files.
		var cmp struct {
			Status string
			Ahead  int `json:"ahead_by"`
			Files  []struct{ Filename, Status, SHA string }
		}
		if err := s.Get("/compare/"+parent+"..."+head.Object.SHA, &cmp); err != nil {
			return entry{}, err
		}
		valid := cmp.Ahead == 1 && len(cmp.Files) == len(files)
		for _, f := range cmp.Files {
			content, ok := files[f.Filename]
			sum := blobID(content)
			valid = valid && ok && f.Status == "added" && sum == f.SHA
		}
		if !valid {
			return blocked("setup branch has unexpected changes; review " + branch), nil
		}
	}
	for _, pr := range prs {
		if pr.State == "open" {
			if exists && head.Object.SHA == pr.Head.SHA {
				return pending(pr.URL), nil
			}
			return blocked("PR head changed; inspect " + pr.URL), nil
		}
		if pr.State == "closed" {
			return blocked("previous proposal closed; inspect " + pr.URL), nil
		}
	}
	var detail strings.Builder
	fmt.Fprintf(&detail, "open a PR against %s", base)
	var entries []object
	for _, name := range sortedKeys(files) {
		fmt.Fprintf(&detail, "\n          + %s\n", name)
		for _, line := range strings.Split(strings.TrimSuffix(files[name], "\n"), "\n") {
			fmt.Fprintf(&detail, "            + %s\n", line)
		}
		entries = append(entries, object{"path": name, "mode": "100644", "type": "blob", "content": files[name]})
	}
	return entry{"CHANGE", strings.TrimRight(detail.String(), "\n"), parent + ":" + head.Object.SHA, func(ctx context.Context, c *github.Client) error {
		if !exists {
			var newTree, newCommit struct{ SHA string }
			if err := c.Write(ctx, "POST", "/git/trees", object{"base_tree": tree, "tree": entries}, &newTree); err != nil {
				return err
			}
			if err := c.Write(ctx, "POST", "/git/commits", object{"message": "[config] Add repository starter files", "tree": newTree.SHA, "parents": []string{parent}}, &newCommit); err != nil {
				return err
			}
			if err := c.Write(ctx, "POST", "/git/refs", object{"ref": "refs/heads/" + branch, "sha": newCommit.SHA}, nil); err != nil {
				return err
			}
		}
		return c.Write(ctx, "POST", "/pulls", object{"title": "Add repository starter files", "body": "Adds the missing files requested by the gh-tune profile. Review the proposed files before merging.", "base": base, "head": branch}, nil)
	}}, nil
}

// Git hashes blobs including their type and byte length.
func blobID(content string) string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))))
}
