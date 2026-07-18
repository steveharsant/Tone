// Package tray puts Tone in the system tray: open settings, approve pairing
// requests, quit. Pure-Go (D-Bus StatusNotifierItem) — no C dependencies.
package tray

import (
	_ "embed"
	"fmt"
	"os/exec"
	"time"

	"fyne.io/systray"

	"github.com/steveharsant/tone/engine/internal/pairing"
)

//go:embed icon.png
var icon []byte

//go:embed appicon.png
var appIcon []byte

// Icon returns the small tray icon PNG (32px).
func Icon() []byte { return icon }

// AppIcon returns the larger application icon PNG (128px) used for the
// desktop entry and packaging.
func AppIcon() []byte { return appIcon }

type Options struct {
	SettingsURL string
	SetupURL    string
	Pairings    *pairing.Store
	// OnQuit stops the engine (cancels the run context).
	OnQuit func()
}

// Run blocks until Quit is chosen. Must be called from the main goroutine.
func Run(opts Options) {
	systray.Run(func() { onReady(opts) }, nil)
}

func onReady(opts Options) {
	systray.SetIcon(icon)
	systray.SetTitle("Tone")
	systray.SetTooltip("Tone — local writing assistant")

	mOpen := systray.AddMenuItem("Open settings", "Open the Tone settings page")
	mSetup := systray.AddMenuItem("Setup wizard", "Re-run first-time setup")
	systray.AddSeparator()
	mPair := systray.AddMenuItem("No pairing requests", "")
	mPair.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit Tone", "Stop the engine")

	// Keep the pairing item current.
	ticker := time.NewTicker(2 * time.Second)
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(opts.SettingsURL)
			case <-mSetup.ClickedCh:
				openBrowser(opts.SetupURL)
			case <-mPair.ClickedCh:
				if opts.Pairings.ApproveOldest() {
					mPair.SetTitle("Pairing approved ✓")
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				if opts.OnQuit != nil {
					opts.OnQuit()
				}
				return
			case <-ticker.C:
				if n := len(opts.Pairings.Pending()); n > 0 {
					mPair.SetTitle(fmt.Sprintf("Approve pairing request (%d pending)", n))
					mPair.Enable()
				} else {
					mPair.SetTitle("No pairing requests")
					mPair.Disable()
				}
			}
		}
	}()
}

func openBrowser(url string) {
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		fmt.Printf("open %s in your browser (xdg-open failed: %v)\n", url, err)
	}
}
