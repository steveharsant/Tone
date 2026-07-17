// Command tone runs the Tone engine: a localhost-only HTTP API that powers
// the browser extension, plus the embedded setup/settings UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/steveharsant/tone/engine/internal/config"
	"github.com/steveharsant/tone/engine/internal/ollama"
	"github.com/steveharsant/tone/engine/internal/server"
)

const version = "0.1.0"

func main() {
	var (
		cfgPath     = flag.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/tone/config.json)")
		port        = flag.Int("port", 0, "override listen port")
		noAutostart = flag.Bool("no-autostart", false, "do not auto-start a managed Ollama on launch")
	)
	flag.Parse()

	if *cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			log.Fatalf("resolve config path: %v", err)
		}
		*cfgPath = p
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if *port != 0 {
		cfg.Port = *port
	}

	dataDir, err := config.DataDir()
	if err != nil {
		log.Fatalf("resolve data dir: %v", err)
	}
	mgr := ollama.NewManager(filepath.Join(dataDir, "ollama"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// If setup already happened against a local Ollama, bring it up in the
	// background so the first check after login doesn't stall.
	if cfg.SetupComplete && cfg.Provider.Type == config.ProviderOllama && !*noAutostart {
		go func() {
			if err := mgr.Start(ctx); err != nil {
				log.Printf("ollama autostart: %v", err)
			}
		}()
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	if cfg.SetupComplete {
		fmt.Printf("Tone engine v%s\n  settings: %s/#%s\n", version, base, cfg.PairingToken)
	} else {
		fmt.Printf("Tone engine v%s — first run\n  setup:    %s/setup#%s\n", version, base, cfg.PairingToken)
	}
	fmt.Printf("  pairing token (for the browser extension): %s\n", cfg.PairingToken)

	srv := server.New(version, cfg, mgr)
	err = srv.ListenAndServe(ctx)
	// Take a supervised Ollama down with us; a user-owned one is untouched.
	mgr.Stop()
	if err != nil {
		log.Fatal(err)
	}
}
