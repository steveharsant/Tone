// Command tone runs the Tone engine: a local HTTP API that powers the
// browser extension, an embedded setup/settings UI, and a system-tray icon.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/steveharsant/tone/engine/internal/config"
	"github.com/steveharsant/tone/engine/internal/ollama"
	"github.com/steveharsant/tone/engine/internal/server"
	"github.com/steveharsant/tone/engine/internal/tray"
)

const version = "0.2.0"

func main() {
	var (
		cfgPath        = flag.String("config", "", "path to config.json (default: $XDG_CONFIG_HOME/tone/config.json)")
		port           = flag.Int("port", 0, "override listen port")
		listen         = flag.String("listen", "", "bind host (default 127.0.0.1). Anything else exposes the engine to your network — token auth still applies, but only do this on a trusted network. Persisted to config.")
		noAutostart    = flag.Bool("no-autostart", false, "do not auto-start a managed Ollama on launch")
		noTray         = flag.Bool("no-tray", false, "run headless without the system-tray icon")
		open           = flag.Bool("open", false, "open the settings page in your browser on start")
		installDesktop = flag.Bool("install-desktop", false, "install a desktop entry (application menu launcher) and exit")
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
	if *listen != "" {
		host := *listen
		if host == "127.0.0.1" || host == "localhost" {
			host = "" // back to the default
		}
		cfg.ListenHost = host
		if err := cfg.Save(); err != nil {
			log.Fatalf("save config: %v", err)
		}
	}

	if *installDesktop {
		if err := installDesktopEntry(); err != nil {
			log.Fatalf("install desktop entry: %v", err)
		}
		fmt.Println("Desktop entry installed — 'Tone' now appears in your application menu.")
		return
	}

	// Single-instance behavior: if an engine already answers on our port,
	// launching again (e.g. from the application menu) just opens settings.
	if engineAlreadyRunning(cfg.Port) {
		url := fmt.Sprintf("http://127.0.0.1:%d/#%s", cfg.Port, cfg.PairingToken)
		fmt.Printf("Tone engine already running — opening %s\n", url)
		exec.Command("xdg-open", url).Start()
		return
	}

	dataDir, err := config.DataDir()
	if err != nil {
		log.Fatalf("resolve data dir: %v", err)
	}
	mgr := ollama.NewManager(filepath.Join(dataDir, "ollama"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.SetupComplete && cfg.Provider.Type == config.ProviderOllama && !*noAutostart {
		go func() {
			if err := mgr.Start(ctx); err != nil {
				log.Printf("ollama autostart: %v", err)
			}
		}()
	}

	srv := server.New(version, cfg, mgr)
	setupURL := fmt.Sprintf("http://127.0.0.1:%d/setup#%s", cfg.Port, cfg.PairingToken)

	if cfg.SetupComplete {
		fmt.Printf("Tone engine v%s\n  settings: %s\n", version, srv.SettingsURL())
	} else {
		fmt.Printf("Tone engine v%s — first run\n  setup:    %s\n", version, setupURL)
	}
	fmt.Printf("  pairing token (for the browser extension): %s\n", cfg.PairingToken)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	if *open {
		go exec.Command("xdg-open", srv.SettingsURL()).Start()
	}

	// Tray wants the main goroutine; fall back to headless when there is no
	// desktop session (SSH, systemd unit, containers) or it was disabled.
	useTray := !*noTray && (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "")
	if useTray {
		go func() {
			err := <-errCh
			mgr.Stop()
			if err != nil {
				log.Fatal(err)
			}
			os.Exit(0)
		}()
		tray.Run(tray.Options{
			SettingsURL: srv.SettingsURL(),
			SetupURL:    setupURL,
			Pairings:    srv.Pairings(),
			OnQuit:      stop,
		})
		// systray.Quit returns here; give the server a moment to drain.
		<-errCh
		mgr.Stop()
		return
	}

	err = <-errCh
	mgr.Stop()
	if err != nil {
		log.Fatal(err)
	}
}

// engineAlreadyRunning reports whether a Tone engine answers on the port.
// Any HTTP response counts — 401s just mean an authenticated route.
func engineAlreadyRunning(port int) bool {
	c := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/health", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// installDesktopEntry writes an XDG launcher + icon so Tone shows up as a
// normal application. User-scoped: ~/.local/share, no elevation.
func installDesktopEntry() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	iconPath := filepath.Join(dataDir, "tone.png")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(iconPath, tray.Icon(), 0o644); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	appsDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return err
	}
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Tone
Comment=Local AI writing assistant
Exec=%s -open
Icon=%s
Terminal=false
Categories=Utility;Office;
`, exe, iconPath)
	return os.WriteFile(filepath.Join(appsDir, "tone.desktop"), []byte(entry), 0o644)
}
