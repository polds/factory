// Command fake-github is the offline GitHub App used by spike/m4.
// It listens for installation tokens, check runs, issue comments, and
// spike commit records. It does not log credentials.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage: fake-github [-addr host:port]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 {
		slog.Error("fake-github: unexpected arguments")
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv, err := newServer("", log)
	if err != nil {
		slog.Error("fake-github: token")
		os.Exit(1)
	}
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("listening", "addr", *addr)
	if err := httpSrv.ListenAndServe(); err != nil {
		slog.Error("fake-github: listen")
		os.Exit(1)
	}
}
