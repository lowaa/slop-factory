// Package protocol is Codex's app-server JSON-RPC protocol: the Go types of
// the messages slop-bot's codex/app-server harness uses, generated from the
// schema of the installed Codex (protocol_gen.go), so they follow Codex
// rather than a hand-copied reading of it.
//
// To regenerate, with the codex you want to pin on PATH:
//
//	go generate ./internal/harness/codex/protocol
//
// then re-record the codec's fixtures (see ../testdata/README.md) and rerun
// the tests: protocol_test.go decodes every recorded message strictly, so a
// field Codex sends that the types lack fails there. To use another message,
// add it to the roots in cmd/gencodexproto.
package protocol

//go:generate go run ../../../../cmd/gencodexproto -out protocol_gen.go
