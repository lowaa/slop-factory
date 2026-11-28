package doctor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
)

// versionRange is a tested range: Min inclusive, Below exclusive. Below is
// the next minor release, because the harnesses ship patch releases daily
// and their protocols have changed at minor ones.
type versionRange struct {
	Min   string
	Below string
}

// tested is every external tool's tested range, in one place. The harness
// ranges start at the versions the spikes and the tier-2 tests ran against
// (2026-11-28); widen one only after running its live and scripted tests
// on the new version.
var tested = map[string]versionRange{
	config.ClaudeCode: {Min: "2.1.332", Below: "2.2"},
	config.Codex:      {Min: "0.172.0", Below: "0.173"},
	config.Pi:         {Min: "1.4.0", Below: "1.5"},
	config.Agy:        {Min: "1.5.18", Below: "1.6"},
	"tmux":            {Min: "3.4"},
}

// minGit is required, not merely tested: the worktree package reads `git
// worktree list --porcelain -z`, which arrived in 2.36.
const minGit = "2.36"

var versionRE = regexp.MustCompile(`\d+(\.\d+)+`)

// parseVersion finds the first dotted version in a --version line, which
// each tool decorates differently ("git version 2.55.0", "codex-cli
// 0.172.0", "2.1.332 (Claude Code)", "tmux 3.4a").
func parseVersion(out string) (string, bool) {
	v := versionRE.FindString(out)
	return v, v != ""
}

// compareVersions compares dotted versions numerically, a missing part
// counting as 0.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		x, y := part(as, i), part(bs, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func part(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(parts[i])
	return n
}

func (r versionRange) contains(v string) bool {
	if r.Min != "" && compareVersions(v, r.Min) < 0 {
		return false
	}
	return r.Below == "" || compareVersions(v, r.Below) < 0
}

func (r versionRange) String() string {
	if r.Below == "" {
		return r.Min + " or later"
	}
	return fmt.Sprintf("%s up to %s", r.Min, r.Below)
}
