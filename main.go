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

// defaultPort is the port the server listens on unless told otherwise. It is fixed, rather than random, because the
// browser keeps the page's settings, such as the theme and the fix prompt, per origin, which includes the port.
const defaultPort = 7826

// listen listens on port on the loopback interface only, since the page shows data from the user's GitHub account.
func listen(ctx context.Context, port int) (net.Listener, error) {
	var listenConfig net.ListenConfig
	return listenConfig.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	flags := flag.NewFlagSet("gh dependabot-triage", flag.ContinueOnError)
	port := flags.Int("port", defaultPort, "port to listen on; 0 picks a random free port")
	org := flags.String("org", "", "only show pull requests in this GitHub organization (or user)")
	noBrowser := flags.Bool("no-browser", false, "do not open the page in a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return fmt.Errorf("create GitHub client (are you logged in? try `gh auth login`): %v", err)
	}
	var opts []ghclient.Option
	if *org != "" {
		opts = append(opts, ghclient.WithOrg(*org))
	}
	github := ghclient.New(gql, opts...)
	srv, err := server.New(logger, github, github)
	if err != nil {
		return err
	}

	portSet := false
	flags.Visit(func(f *flag.Flag) {
		portSet = portSet || f.Name == "port"
	})
	listener, err := listen(ctx, *port)
	if err != nil && !portSet {
		// The default port is taken, probably by another instance. A random port works, but the page loses what
		// it keeps in the browser's storage, which is per origin and so per port.
		logger.Warn(
			"default port is in use, using a random port; saved settings such as the theme are not available",
			"port",
			*port,
			"error",
			err,
		)
		listener, err = listen(ctx, 0)
	}
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
