package setup

import (
	"context"
	"strings"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
)

const readContributionAccess = `
query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    id
    hasIssuesEnabled
    hasPullRequestsEnabled
    issueCreationPolicy
    pullRequestCreationPolicy
  }
}`

const updateContributionAccess = `
mutation($input: UpdateRepositoryInput!) {
  updateRepository(input: $input) { repository { id } }
}`

func contributionAccess(issues, pulls *bool, issuePolicy, pullPolicy string) job {
	wanted := object{}
	if issues != nil {
		wanted["hasIssuesEnabled"] = *issues
	}
	if pulls != nil {
		wanted["hasPullRequestsEnabled"] = *pulls
	}
	if issuePolicy != "" {
		wanted["issueCreationPolicy"] = strings.ToUpper(issuePolicy)
	}
	if pullPolicy != "" {
		wanted["pullRequestCreationPolicy"] = strings.ToUpper(pullPolicy)
	}
	return setting("Issue and PR access", func(s *github.Snapshot) (entry, error) {
		var result struct{ Repository object }
		if err := s.QueryRepository(readContributionAccess, &result); err != nil {
			return entry{}, err
		}
		id, err := field(result.Repository, "id")
		if err != nil {
			return entry{}, err
		}
		before := object{}
		input := object{"repositoryId": id}
		for _, key := range sortedKeys(wanted) {
			current, err := field(result.Repository, key)
			if err != nil {
				return entry{}, err
			}
			before[key] = current
			input[key] = wanted[key]
		}
		desired := describeContributionAccess(wanted)
		if encode(before) == encode(wanted) {
			return match(desired), nil
		}
		// Set enablement and its creation policy in the same repository update.
		variables := object{"input": input}
		return entry{
			state:  "CHANGE",
			detail: describeContributionAccess(before) + " → " + desired,
			guard:  encode(input) + encode(before),
			write: func(ctx context.Context, c *github.Client) error {
				return c.GraphQL(ctx, updateContributionAccess, variables, nil)
			},
		}, nil
	})
}

func describeContributionAccess(values object) string {
	var parts []string
	for _, feature := range []struct{ label, enabled, policy string }{
		{"Issues", "hasIssuesEnabled", "issueCreationPolicy"},
		{"PRs", "hasPullRequestsEnabled", "pullRequestCreationPolicy"},
	} {
		enabled, hasEnabled := values[feature.enabled].(bool)
		policy, hasPolicy := values[feature.policy].(string)
		if !hasEnabled && !hasPolicy {
			continue
		}
		part := feature.label
		if hasEnabled {
			part += " " + strings.ToLower(onOff(enabled))
		}
		if hasPolicy {
			switch policy {
			case "ALL":
				policy = "anyone"
			case "COLLABORATORS_ONLY":
				policy = "collaborators only"
			}
			part += " (" + policy + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}
