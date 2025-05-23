// main executable.
package main

import (
	"net/http"
	"os"

	_ "net/http/pprof"

	"github.com/bluenviron/mediamtx/internal/core"
)

func main() {
	s, ok := core.New(os.Args[1:])
	if !ok {
		os.Exit(1)
	}
	go func() {
		http.ListenAndServe(":6060", nil)
	}()
	s.Wait()
}
