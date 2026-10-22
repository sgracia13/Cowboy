package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/hooks"
)

// hooksFromEnv reads EVENT_HOOKS, EVENT_HOOK_SECRET and EVENT_HOOK_TYPES.
func hooksFromEnv() (hooks.Config, error) {
	return hooks.Parse(os.Getenv("EVENT_HOOKS"), os.Getenv("EVENT_HOOK_SECRET"), os.Getenv("EVENT_HOOK_TYPES"))
}

// wireHooks turns on outbound events when EVENT_HOOKS is set, and gives
// events a link to each pull request.
func wireHooks(a *app.App) {
	cfg, err := hooksFromEnv()
	if err != nil {
		log.Fatalf("EVENT_HOOKS: %v", err)
	}
	a.PRLink = prLink(env("FORGE", "github"), os.Getenv("FORGE_URL"))
	if len(cfg.URLs) == 0 {
		return
	}
	if cfg.Secret == "" {
		log.Print("event hooks: EVENT_HOOK_SECRET isn't set, so receivers can't tell events are really from Cowboy")
	}
	a.Hooks = hooks.New(cfg)
	what := "every event"
	if cfg.Only != nil {
		what = os.Getenv("EVENT_HOOK_TYPES")
	}
	log.Printf("event hooks: sending %s to %d receiver(s)", what, len(cfg.URLs))
}

// prLink builds the web link to a pull request on the configured forge.
func prLink(kind, forgeURL string) func(repo string, n int) string {
	base := strings.TrimRight(forgeURL, "/")
	switch strings.ToLower(kind) {
	case "gitlab":
		if base == "" {
			base = "https://gitlab.com"
		}
		base = strings.TrimSuffix(base, "/api/v4")
		return func(repo string, n int) string { return fmt.Sprintf("%s/%s/-/merge_requests/%d", base, repo, n) }
	case "gitea", "forgejo":
		if base == "" {
			return nil
		}
		base = strings.TrimSuffix(base, "/api/v1")
		return func(repo string, n int) string { return fmt.Sprintf("%s/%s/pulls/%d", base, repo, n) }
	default:
		if base == "" {
			base = "https://github.com"
		}
		base = strings.TrimSuffix(base, "/api/v3")
		if base == "https://api.github.com" {
			base = "https://github.com"
		}
		return func(repo string, n int) string { return fmt.Sprintf("%s/%s/pull/%d", base, repo, n) }
	}
}

// cowboy hooks test: send one test event to every EVENT_HOOKS receiver and
// say whether it landed.
func hooksCmd(args []string) {
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprintln(os.Stderr, "usage: cowboy hooks test   (sends a test event to every EVENT_HOOKS receiver)")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("hooks test", flag.ExitOnError)
	fs.Parse(args[1:])
	cfg, err := hooksFromEnv()
	if err != nil {
		log.Fatalf("EVENT_HOOKS: %v", err)
	}
	if len(cfg.URLs) == 0 {
		log.Fatal("EVENT_HOOKS isn't set: give it one or more receiver URLs, comma-separated")
	}
	cfg.Only = nil // a test always goes
	s := hooks.New(cfg)
	s.Send(hooks.Event{Type: hooks.Test, Repo: "example/repo", Number: 1,
		Data: map[string]string{"message": "Cowboy can reach you. Real events look like this, with a pr object."},
		PR:   &hooks.PR{Title: "Test event", State: "open", Pen: "normal"}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.Close(ctx)
	st := s.Stats()
	signed := "signed"
	if cfg.Secret == "" {
		signed = "unsigned (set EVENT_HOOK_SECRET)"
	}
	fmt.Printf("test event %s: delivered to %d of %d receiver(s)\n", signed, st.Sent, len(cfg.URLs))
	if st.Failed > 0 {
		fmt.Printf("last error: %s\n", st.LastError)
		os.Exit(1)
	}
}
