// Command tinygo-check is a build probe, not a tool: its empty main imports
// the errors, protocol, transport and types packages only for their side
// effects, so compiling it with TinyGo shows whether those packages still
// build for constrained targets. Running it does nothing.
package main

import (
	_ "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	_ "github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	_ "github.com/Project-Helianthus/helianthus-ebusgo/transport"
	_ "github.com/Project-Helianthus/helianthus-ebusgo/types"
)

func main() {}
