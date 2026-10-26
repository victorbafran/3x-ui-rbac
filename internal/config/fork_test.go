package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Merging an upstream release brings back its defaults, and an updater left on
// upstream installs the plain panel over this fork. Every place must say fork.
func TestEveryUpdaterDefaultIsThisFork(t *testing.T) {
	repo := regexp.MustCompile(`xui_repo="\$\{XUI_REPO:=([^}]+)\}"`)
	branch := regexp.MustCompile(`xui_branch="\$\{XUI_BRANCH:=([^}]+)\}"`)
	for _, script := range []string{"install.sh", "update.sh", "x-ui.sh"} {
		body := readRepoFile(t, script)
		if m := repo.FindStringSubmatch(body); m == nil || m[1] != ForkRepo {
			t.Errorf("%s: default repository is %v, want %s", script, m, ForkRepo)
		}
		if m := branch.FindStringSubmatch(body); m == nil || m[1] != ForkBranch {
			t.Errorf("%s: default branch is %v, want %s", script, m, ForkBranch)
		}
	}
	panel := readRepoFile(t, filepath.Join("internal", "web", "service", "panel", "panel.go"))
	if strings.Contains(panel, "MHSanaei/3x-ui") {
		t.Error("the panel's updater names the upstream repository")
	}
}

func TestTheUpdaterHonoursTheRecordedRepository(t *testing.T) {
	t.Setenv("XUI_REPO", "")
	t.Setenv("XUI_BRANCH", "")
	if UpdateRepo() != ForkRepo || UpdateBranch() != ForkBranch {
		t.Errorf("defaults %s@%s, want %s@%s", UpdateRepo(), UpdateBranch(), ForkRepo, ForkBranch)
	}
	t.Setenv("XUI_REPO", " someone/3x-ui-fork ")
	t.Setenv("XUI_BRANCH", "main")
	if UpdateRepo() != "someone/3x-ui-fork" || UpdateBranch() != "main" {
		t.Errorf("recorded %s@%s, want someone/3x-ui-fork@main", UpdateRepo(), UpdateBranch())
	}
}

func TestTheReleaseTagFallsBackToTheBaseVersion(t *testing.T) {
	saved := releaseTag
	t.Cleanup(func() { releaseTag = saved })
	releaseTag = ""
	if got := GetReleaseTag(); got != GetBaseVersion() {
		t.Errorf("an untagged build reports %q, want the base version %q", got, GetBaseVersion())
	}
	releaseTag = "v3.10.2-rbac42"
	if got := GetReleaseTag(); got != "v3.10.2-rbac42" {
		t.Errorf("a tagged build reports %q", got)
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
