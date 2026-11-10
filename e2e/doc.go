// Package e2e holds the tests that drive the built holdfast binary the way a
// person and an agent do: the command line in a real folder, the MCP server
// over a real pipe, and the ledger over real HTTP. Nothing here reaches into
// the program; if these pass, the thing that ships works.
//
// The tests build the binary themselves. To test one that is already built
// (the one a release is about to publish, say), name it:
//
//	HOLDFAST_BIN=$PWD/bin/holdfast go test ./e2e
package e2e
