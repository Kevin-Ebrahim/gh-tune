// Package setup compares a profile to a repository, then executes verified changes.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
)

type object = map[string]any
type entry struct {
	state, detail string
	// guard records write inputs not visible in the human-readable diff.
	guard string
	write func(context.Context, *github.Client) error
}

func match(detail string) entry   { return entry{state: "MATCH", detail: detail} }
func blocked(detail string) entry { return entry{state: "BLOCKED", detail: detail} }
func skip(detail string) entry    { return entry{state: "N/A", detail: detail} }
func pending(detail string) entry { return entry{state: "PENDING", detail: detail} }

type job struct {
	name    string
	after   string
	inspect func(*github.Snapshot) (entry, error)
}
type item struct {
	job
	entry
}
type Plan struct{ items []item }

func inspect(j job, s *github.Snapshot) entry {
	e, err := j.inspect(s)
	if err != nil {
		return blocked(err.Error())
	}
	return e
}
func Build(ctx context.Context, c *github.Client, p profile.Profile) (Plan, error) {
	s := c.Snapshot(ctx)
	var repo repositoryContext
	if err := s.Get("", &repo); err != nil {
		return Plan{}, err
	}
	if repo.Archived || repo.Disabled {
		return Plan{}, fmt.Errorf("repository is archived or disabled")
	}
	jobs := settings(p)
	if p.Baseline == "github" {
		jobs = creationDefaults(s, repo, c.Repo)
	}
	if p.Branch != nil {
		jobs = append(jobs, branchJob(*p.Branch))
	}
	if p.Files.IssueTemplate || p.Files.PRTemplate || len(p.Files.Dependabot) > 0 {
		jobs = append(jobs, filesJob(p, c.Repo))
	}
	var plan Plan
	for _, j := range jobs {
		plan.items = append(plan.items, item{j, inspect(j, s)})
	}
	return plan, nil
}
func (p Plan) Print(w io.Writer) {
	for _, i := range p.items {
		fmt.Fprintf(w, "%-9s %-35s %s\n", i.state, i.name, i.detail)
	}
}
func (p Plan) Exit(check bool) int {
	code := 0
	for _, i := range p.items {
		switch i.state {
		case "BLOCKED":
			return 1
		case "CHANGE", "PENDING", "AFTER":
			if check {
				code = 2
			}
		}
	}
	return code
}
func (p Plan) Apply(ctx context.Context, c *github.Client) Plan {
	result := Plan{}
	done := map[string]bool{}
	for _, i := range p.items {
		e := i.entry
		if e.state == "CHANGE" || e.state == "AFTER" {
			switch {
			case i.after != "" && !done[i.after]:
				e = blocked("prerequisite incomplete: " + i.after)
			default:
				e = applyChange(ctx, c, i)
			}
		}
		if e.state == "AFTER" {
			e = blocked(e.detail)
		}
		done[i.name] = e.state == "APPLIED" || e.state == "MATCH"
		result.items = append(result.items, item{i.job, e})
	}
	return result
}
func applyChange(ctx context.Context, c *github.Client, i item) entry {
	current := inspect(i.job, c.Snapshot(ctx))
	if current.state != "CHANGE" {
		return current
	}
	if i.state != "AFTER" && (current.guard != i.guard || current.detail != i.detail) {
		return blocked("changed during apply; rerun to review")
	}
	err := current.write(ctx, c)
	// A timed-out write may have succeeded. Observe state before reporting failure.
	verified := inspect(i.job, c.Snapshot(ctx))
	switch verified.state {
	case "MATCH":
		verified.state = "APPLIED"
		return verified
	case "PENDING":
		return verified
	}
	if err != nil {
		return blocked(err.Error())
	}
	return blocked("write could not be verified: " + verified.detail)
}
func change(detail, method, path string, body any) entry {
	b, _ := json.Marshal(body)
	return entry{"CHANGE", detail, method + " " + path + " " + string(b), func(ctx context.Context, c *github.Client) error { return c.Write(ctx, method, path, body, nil) }}
}
func encode(v any) string { b, _ := json.Marshal(v); return string(b) }
func diff(label string, before, after any) string {
	return fmt.Sprintf("%s %s → %s", label, encode(before), encode(after))
}
func field(m object, key string) (any, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, fmt.Errorf("GitHub did not expose %s", key)
	}
	return v, nil
}
func readObject(s *github.Snapshot, path string) (object, error) {
	var m object
	err := s.Get(path, &m)
	return m, err
}
func setting(name string, fn func(*github.Snapshot) (entry, error)) job {
	return job{name: name, inspect: fn}
}
func projection(name, endpoint, method string, wanted object, keep ...string) job {
	return setting(name, func(s *github.Snapshot) (entry, error) {
		m, err := readObject(s, endpoint)
		if err != nil {
			return entry{}, err
		}
		body := object{}
		var changes []string
		for _, key := range sortedKeys(wanted) {
			old, err := field(m, key)
			if err != nil {
				return entry{}, err
			}
			if encode(old) != encode(wanted[key]) {
				body[key] = wanted[key]
				changes = append(changes, diff(key, old, wanted[key]))
			}
		}
		if len(changes) == 0 {
			return match("as requested"), nil
		}
		for _, key := range keep {
			if _, ok := body[key]; !ok {
				v, err := field(m, key)
				if err != nil {
					return entry{}, err
				}
				body[key] = v
			}
		}
		return change(strings.Join(changes, ", "), method, endpoint, body), nil
	})
}
