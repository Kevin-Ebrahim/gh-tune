package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/Kevin-Ebrahim/gh-tune/internal/github"
	"github.com/Kevin-Ebrahim/gh-tune/internal/profile"
	"github.com/Kevin-Ebrahim/gh-tune/internal/setup"
)

const usage = `Usage: gh tune OWNER/REPO --profile NAME|FILE [--dry-run | --apply | --check]

Profiles: default, solo, team, opensource, or a local YAML file.
  --dry-run   Preview the changes (default).
  --apply     Apply settings, verify results, and propose missing files in a PR.
  --check     Exit 0 when settings match, 2 for drift, 1 for incomplete checks.
  --version   Print the build version.
`

var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

func run(ctx context.Context, args []string, out, errs io.Writer) int {
	fail := func(err any) int { fmt.Fprintln(errs, err); return 1 }
	var repo, name, mode string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprint(out, usage)
			return 0
		case arg == "--version":
			v := "development"
			if b, ok := debug.ReadBuildInfo(); ok && b.Main.Version != "" {
				v = b.Main.Version
			}
			fmt.Fprintln(out, "gh-tune", v)
			return 0
		case arg == "--profile" || strings.HasPrefix(arg, "--profile="):
			if name != "" {
				return fail("provide one profile")
			}
			if arg == "--profile" {
				i++
				if i == len(args) {
					return fail("--profile requires a name or file")
				}
				name = args[i]
			} else {
				name = strings.TrimPrefix(arg, "--profile=")
			}
		case arg == "--dry-run" || arg == "--apply" || arg == "--check":
			if mode != "" {
				return fail("choose one mode")
			}
			mode = arg
		case !strings.HasPrefix(arg, "-") && repo == "":
			repo = arg
		default:
			return fail("unexpected argument: " + arg)
		}
	}
	if !repoName.MatchString(repo) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/..") || name == "" {
		return fail(usage)
	}
	if mode == "" {
		mode = "--dry-run"
	}
	p, err := profile.Load(name)
	if err != nil {
		return fail(err)
	}
	c, err := github.New(repo)
	if err != nil {
		return fail(err)
	}
	plan, err := setup.Build(ctx, c, p)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(out, "%s  profile %s  %s\n", repo, name, strings.TrimPrefix(mode, "--"))
	if mode == "--apply" {
		plan = plan.Apply(ctx, c)
	}
	plan.Print(out)
	return plan.Exit(mode == "--check")
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
