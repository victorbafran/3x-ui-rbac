package config

import (
	"os"
	"strings"
)

// This fork's own repository. The updater installs its releases, never
// upstream's, which would quietly drop every feature the fork carries.
const (
	ForkRepo   = "victorbafran/3x-ui-rbac"
	ForkBranch = "rbac"
)

// releaseTag is the tag a release was built from, set with -ldflags -X by the
// release workflow; empty for a local build.
var releaseTag string

// UpdateRepo is where the panel's updater takes its releases and scripts from:
// XUI_REPO as install.sh recorded it, else this fork.
func UpdateRepo() string {
	if repo := strings.TrimSpace(os.Getenv("XUI_REPO")); repo != "" {
		return repo
	}
	return ForkRepo
}

// UpdateBranch is the branch of UpdateRepo the updater's scripts come from.
func UpdateBranch() string {
	if branch := strings.TrimSpace(os.Getenv("XUI_BRANCH")); branch != "" {
		return branch
	}
	return ForkBranch
}

// GetReleaseTag is the tag this binary was released as, e.g. v3.10.2-rbac42,
// else the embedded base version; the updater compares fork releases by it.
func GetReleaseTag() string {
	if tag := strings.TrimSpace(releaseTag); tag != "" {
		return tag
	}
	return GetBaseVersion()
}
