// Command runner-api serves the sandbox runner's HTTP API on port 8080.
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/abrudz/sandbox/internal/api"
	"github.com/abrudz/sandbox/internal/sandbox"
)

func main() {
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           api.NewHandler(api.Options{Executor: sandbox.Nsjail{}}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("runner-api listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}
