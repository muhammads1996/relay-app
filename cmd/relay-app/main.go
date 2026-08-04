// Command relay-app is the Relay desktop application: a native window
// (system webview via Wails v2 — WebView2/WKWebKit/WebKitGTK, no bundled
// Chromium) hosting the same embedded UI and Go engine that `relay ui`
// serves. The webview talks to internal/ui's handler directly through the
// Wails asset server, so the desktop app and the browser UI are one
// codebase.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/store"
	"github.com/muhaymien96/relay/internal/ui"
)

// defaultWorkspace returns the OS-standard location for Relay's data:
//
//	Windows : %APPDATA%\Relay
//	macOS   : ~/Library/Application Support/Relay
//	Linux   : ~/.config/Relay  (or $XDG_CONFIG_HOME/Relay)
func defaultWorkspace() string {
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "Relay")
	}
	// os.UserConfigDir failed (unusual); fall back to home directory.
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Relay")
	}
	return "Relay"
}

// migrateFromHome moves an existing ~/Relay workspace to newRoot when
// newRoot does not yet exist. This handles the one-time transition from the
// old default. On failure it logs a message and continues; the user keeps
// their data at the old path and a fresh database opens at newRoot.
func migrateFromHome(newRoot string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	old := filepath.Join(home, "Relay")
	if filepath.Clean(old) == filepath.Clean(newRoot) {
		return // same path on this platform (e.g. some Linux setups)
	}
	if _, err := os.Stat(filepath.Join(old, "relay.db")); err != nil {
		return // no previous data to migrate
	}
	if _, err := os.Stat(newRoot); err == nil {
		return // destination already exists — never overwrite
	}
	// Rename the whole directory. On the same filesystem (the common case:
	// %APPDATA% and ~ are both on C:\) this is atomic and instant.
	if err := os.MkdirAll(filepath.Dir(newRoot), 0o755); err != nil {
		return
	}
	if err := os.Rename(old, newRoot); err != nil {
		fmt.Fprintf(os.Stderr,
			"relay-app: could not migrate ~/Relay to %s: %v\n"+
				"  Your data is still at %s — set RELAY_WORKSPACE to keep using it.\n",
			newRoot, err, old)
	}
}

func main() {
	workspace := flag.String("workspace", "", "workspace directory (default: $RELAY_WORKSPACE or OS app-data dir)")
	flag.Parse()

	root := *workspace
	if root == "" {
		root = os.Getenv("RELAY_WORKSPACE")
	}
	if root == "" && flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	if root == "" {
		root = defaultWorkspace()
		// One-time migration: move ~/Relay → OS app-data dir if it exists
		// and the new location is not yet initialised.
		migrateFromHome(root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay-app:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "relay-app:", err)
		os.Exit(1)
	}
	db, err := store.Open(filepath.Join(abs, "relay.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay-app:", err)
		os.Exit(1)
	}
	defer db.Close()
	if empty, err := db.Empty(); err == nil && empty {
		_, _ = db.SeedFromDir(abs)
	}

	srv := &ui.Server{DB: db, Engine: engine.NewOptions()}
	err = wails.Run(&options.App{
		Title:     "Relay — " + filepath.Base(abs),
		Width:     1280,
		Height:    820,
		MinWidth:  760,
		MinHeight: 480,
		Mac:       &mac.Options{DisableZoom: false},
		// No embedded asset FS: every request (index and /api/*) routes to
		// the same handler `relay ui` serves over localhost.
		AssetServer: &assetserver.Options{Handler: srv.Handler()},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay-app:", err)
		os.Exit(1)
	}
}
