package fake_test

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
)

func TestMain(m *testing.M) { faketest.Main(m) }

func TestConformance(t *testing.T) {
	t.Parallel()
	conformance.Run(t, faketest.RPCSubject)
}

// The same suite as fake/rpc: the proof that the core can't tell the two
// kits apart.
func TestConformanceTUI(t *testing.T) {
	t.Parallel()
	conformance.Run(t, faketest.TUISubject)
}
