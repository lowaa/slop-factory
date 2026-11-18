package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// BundleHashFile records, in a workspace, the hash of the bundle its last
// setup or update installed. The hooks and the supervisor read it back, so
// the server can tell which workspaces are behind.
const BundleHashFile = ".slop-haus/bundle-hash"

// bundleHashLen is how many hex characters of the digest are kept: enough
// that two bundles never collide in practice, short enough to read on a screen.
const bundleHashLen = 16

type bundlePart struct {
	path string
	body []byte
}

// hashBundle is the digest of the parts, in order.
func hashBundle(parts []bundlePart) string {
	sum := sha256.New()
	for _, part := range parts {
		// Hashing each body first, with its path, keeps a byte moved from
		// one part to the next from hashing the same.
		inner := sha256.Sum256(part.body)
		sum.Write([]byte(part.path))
		sum.Write([]byte{0})
		sum.Write(inner[:])
	}
	return hex.EncodeToString(sum.Sum(nil))[:bundleHashLen]
}

// bundleParts are the static assets and the briefing protocol text, which an
// update installs unchanged into every workspace. The briefings are rendered
// for a fixed agent name, with no fleet instructions (those are the owners',
// not a release's), so only a change to the protocol itself moves the hash.
func bundleParts() []bundlePart {
	parts := []bundlePart{
		{HookScript, hookScript},
		{SessionStartHookScript, sessionStartHookScript},
		{UsageHookScript, usageHookScript},
		{UserPromptHookScript, userPromptHookScript},
		{UsageReporter, usageReporter},
		{SessionDescriber, sessionDescriber},
		{PiExtension, piExtension},
		{TokenModule, tokenModule},
		{TokenScript, tokenScript},
		{Updater, updaterScript},
		{RepoSyncer, repoSyncerScript},
		{AppInstaller, appInstallerScript},
		{RemoteControlLauncher, remoteControlLauncherScript},
		{"worker-rules.md", []byte(workerRules)},
		{"splitting.md", []byte(splittingProtocol)},
	}
	for _, changeTools := range []bool{false, true} {
		docs := Documents("agent", nil, changeTools)
		parts = append(parts, bundlePart{HeadlessBriefing, []byte(docs.Headless.Text())})
		for _, role := range []Role{RoleHeadless, RoleConductor, RoleConsole} {
			parts = append(parts, bundlePart{AgentsMD, []byte(RootAgentsText(Params{AgentName: "agent", Role: role}, docs))})
		}
	}
	parts = append(parts, bundlePart{ClaudeMD, []byte(RootClaudeMDText())})
	return parts
}

var bundleHash = sync.OnceValue(func() string { return hashBundle(bundleParts()) })

// BundleHash identifies the managed asset bundle this server hands out: the
// hooks, scripts, pi extension and briefing rules, which are the same for
// every workspace. A workspace's own settings, which vary by agent, are not
// part of it.
func BundleHash() string { return bundleHash() }

// Bundle statuses: how a reported bundle hash compares with BundleHash.
const (
	BundleCurrent = "current"
	BundleStale   = "stale"
	BundleUnknown = "unknown"
)

// BundleStatusOf compares a reported hash with the bundle served now. A
// workspace that reports none is unknown rather than stale: it may predate
// the file, and claiming it is behind would be a guess.
func BundleStatusOf(reported string) string {
	switch reported {
	case "":
		return BundleUnknown
	case BundleHash():
		return BundleCurrent
	default:
		return BundleStale
	}
}
