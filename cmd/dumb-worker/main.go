// dumb-worker pulls jobs from dumb-server and runs them on the local Ollama.
// It accepts no inbound connections.
package main

import (
	"fmt"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func main() {
	fmt.Printf("dumb-worker: not implemented yet (protocol v%d)\n", protocol.Version)
}
