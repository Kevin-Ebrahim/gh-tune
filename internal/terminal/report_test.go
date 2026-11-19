package terminal

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Kevin-Ebrahim/gh-tune/internal/setup"
)

func TestPipedReportShowsTargetAndValues(t *testing.T) {
	var b bytes.Buffer
	d := New(&b)
	d.Header("owner/repo", "default", "Dry run")
	stop := d.Loading(t.Context(), "Reading repository settings")
	stop()
	d.Report([]setup.Result{
		{Status: "MATCH", Setting: "secret scanning", Detail: "Enabled"},
		{Status: "CHANGE", Setting: "Wiki", Detail: "Disabled → Enabled"},
		{Status: "BLOCKED", Setting: "Ruleset", Detail: "Missing permission"},
	}, "Dry run")
	text := b.String()
	for _, part := range []string{"owner/repo", "Target profile  default", "Dry run", "Needs attention  (1)", "Missing permission", "Planned changes  (1)", "Disabled → Enabled", "Matching settings  (1)", "Secret scanning", "Enabled"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %q in %s", part, text)
		}
	}
	if strings.Index(text, "Missing permission") > strings.Index(text, "Planned changes") || strings.Index(text, "Planned changes") > strings.Index(text, "Matching settings") {
		t.Fatal(text)
	}
	if strings.ContainsAny(text, "\r\x1b") || strings.Contains(text, "Reading repository") {
		t.Fatal(text)
	}
}

func TestSpinnerCleanup(t *testing.T) {
	t.Setenv("GH_SPINNER_DISABLED", "0")
	var b bytes.Buffer
	d := Display{out: &b, width: 80, tty: true}
	ctx, cancel := context.WithCancel(t.Context())
	stop := d.Loading(ctx, "Checking settings")
	cancel()
	stop()
	if !strings.HasPrefix(b.String(), "\r⠋ Checking settings") || !strings.HasSuffix(b.String(), "\r"+strings.Repeat(" ", 19)+"\r") {
		t.Fatalf("%q", b.String())
	}
	b.Reset()
	t.Setenv("GH_SPINNER_DISABLED", "1")
	d.Loading(t.Context(), "Checking settings")()
	if b.String() != "Checking settings\n" {
		t.Fatal(b.String())
	}
}

func TestNarrowApplyReportPreservesDetails(t *testing.T) {
	var b bytes.Buffer
	d := Display{out: &b, width: 40}
	d.Report([]setup.Result{
		{Status: "APPLIED", Setting: "Dependabot alerts", Detail: "Disabled"},
		{Status: "PENDING", Setting: "Repository files", Detail: "https://github.com/owner/repo/pull/1"},
		{Status: "N/A", Setting: "Private vulnerability reporting", Detail: "Public repositories only"},
	}, "Apply")
	for _, want := range []string{"Applied and verified  (1)", "Dependabot alerts\n    Disabled", "Awaiting PR review  (1)", "https://github.com/owner/repo/pull/1", "Not applicable  (1)"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing %s in %s", want, b.String())
		}
	}
}
