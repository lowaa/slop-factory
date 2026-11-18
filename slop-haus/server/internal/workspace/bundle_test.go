package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The hash is what tells a stale workspace from a current one, so any change
// to what an update installs has to change it.
func TestBundleHash_DependsOnEveryPartAndItsPath(t *testing.T) {
	base := []bundlePart{{"a", []byte("one")}, {"b", []byte("two")}}
	want := hashBundle(base)

	assert.Equal(t, want, hashBundle([]bundlePart{{"a", []byte("one")}, {"b", []byte("two")}}), "the same parts hash the same")
	assert.NotEqual(t, want, hashBundle([]bundlePart{{"a", []byte("one!")}, {"b", []byte("two")}}), "an edited body")
	assert.NotEqual(t, want, hashBundle([]bundlePart{{"a", []byte("one")}}), "a part dropped")
	assert.NotEqual(t, want, hashBundle([]bundlePart{{"a", []byte("one")}, {"c", []byte("two")}}), "a part renamed")
	assert.NotEqual(t, want, hashBundle([]bundlePart{{"a", []byte("onetwo")}, {"b", nil}}), "a byte moved to the next part")
	assert.Len(t, want, bundleHashLen)
}

// What the bundle covers is a list a person can forget to extend, so the
// installed assets are checked against it.
func TestBundleParts_CoverEveryManagedAssetAndTheBriefings(t *testing.T) {
	covered := map[string]bool{}
	for _, p := range bundleParts() {
		covered[p.path] = true
	}
	for _, path := range []string{
		HookScript, SessionStartHookScript, UsageHookScript, UserPromptHookScript, UsageReporter, SessionDescriber,
		PiExtension, TokenModule, TokenScript, Updater, RepoSyncer, AppInstaller, RemoteControlLauncher,
		HeadlessBriefing, AgentsMD, ClaudeMD,
	} {
		assert.True(t, covered[path], path)
	}
}

// The hash is written last, so an update that stops part way leaves the old
// one and the workspace stays reported as behind.
func TestFiles_TheBundleHashIsWrittenLast(t *testing.T) {
	files, err := Files(Params{AgentName: "a", Server: "https://s"})
	assert.NoError(t, err)
	assert.Equal(t, BundleHashFile, files[len(files)-1].Path)
}
