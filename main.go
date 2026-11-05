// Command gh-dependabot-triage is a GitHub CLI extension that opens a local web page listing the Dependabot
// pull requests you have been asked to review.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
	"github.com/Edholm/gh-dependabot-triage/internal/server"
	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/browser"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	flags := flag.NewFlagSet("gh dependabot-triage", flag.ContinueOnError)
	port := flags.Int("port", 0, "port to listen on (default: a random free port)")
	noBrowser := flags.Bool("no-browser", false, "do not open the page in a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return fmt.Errorf("create GitHub client (are you logged in? try `gh auth login`): %v", err)
	}
	github := ghclient.New(gql)
	srv, err := server.New(logger, github, github)
	if err != nil {
		return err
	}

	// Only listen on the loopback interface: the page shows data from the user's GitHub account.
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		return fmt.Errorf("listen: %v", err)
	}
	url := "http://" + listener.Addr().String()
	fmt.Fprintf(os.Stderr, "Serving on %s (press Ctrl-C to stop)\n", url)

	if !*noBrowser {
		if err := browser.New("", os.Stdout, os.Stderr).Browse(url); err != nil {
			logger.Warn("could not open a browser", "url", url, "error", err)
		}
	}
	return srv.Serve(ctx, listener)
}
