// dumb-server queues AI jobs and hands them to workers that long-poll for them.
package main

import (
	"fmt"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func main() {
	fmt.Printf("dumb-server: not implemented yet (protocol v%d)\n", protocol.Version)
}
