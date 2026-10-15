// Command fake-slack is the offline Slack Web API used by spike/m4.
// It serves chat.postMessage and chat.update. It does not log the bot token.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	tokenFile := flag.String("token-file", "", "file containing the expected bot token")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(flag.CommandLine.Output(), "Usage: fake-slack [-addr host:port] [-token-file PATH]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 {
		slog.Error("fake-slack: unexpected arguments")
		os.Exit(2)
	}
	token := ""
	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			slog.Error("fake-slack: token file unreadable")
			os.Exit(1)
		}
		token = strings.TrimSpace(string(raw))
		if token == "" {
			slog.Error("fake-slack: token file is empty")
			os.Exit(1)
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           newSlack(token, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("listening", "addr", *addr)
	if err := httpSrv.ListenAndServe(); err != nil {
		slog.Error("fake-slack: listen")
		os.Exit(1)
	}
}
