// Command wittydvd-module is WittyDVD as a helper process, one for each open disc, so
// a new version of the engine can reach a server without a new server. See WittyModule
// for the protocol and package wire for what is said over it.
package main

import (
	"github.com/wittywilla/wittydvd"
	"github.com/wittywilla/wittydvd/diagnose"
	"github.com/wittywilla/wittydvd/wire"
	"github.com/wittywilla/wittymodule"
)

func main() {
	h := &diagnose.Handler{Next: new(wire.Helper).Handle}
	wittymodule.Main(wittydvd.ManifestJSON, h.Handle)
}
