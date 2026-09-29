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
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/muhaymien96/relay/internal/engine"
	"github.com/muhaymien96/relay/internal/secretstore"
	"github.com/muhaymien96/relay/internal/store"
	"github.com/muhaymien96/relay/internal/ui"
	workspacepkg "github.com/muhaymien96/relay/internal/workspace"
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
func migrateFromHome(newRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return newRoot, nil
	}
	old := filepath.Join(home, "Relay")
	if filepath.Clean(old) == filepath.Clean(newRoot) {
		return newRoot, nil
	}
	if _, err := os.Stat(filepath.Join(old, "relay.db")); err != nil {
		return newRoot, nil
	}
	if _, err := os.Stat(newRoot); err == nil {
		return newRoot, nil
	}
	if err := os.MkdirAll(filepath.Dir(newRoot), 0o755); err != nil {
		return old, fmt.Errorf("could not prepare %s: %w; continuing with existing workspace %s", newRoot, err, old)
	}
	if err := os.Rename(old, newRoot); err != nil {
		return old, fmt.Errorf("could not migrate old workspace to %s: %w; continuing with existing workspace %s", newRoot, err, old)
	}
	return newRoot, nil
}
func main() {
	workspace := flag.String("workspace", "", "workspace directory (default: $RELAY_WORKSPACE or OS app-data dir)")
	migrateXray := flag.Bool("migrate-xray-credentials", false, "move legacy Xray credentials from SQLite to Windows Credential Manager, then exit")
	xrayBackup := flag.String("xray-backup", "", "new full-database backup path required for --migrate-xray-credentials")
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
		var migrationErr error
		root, migrationErr = migrateFromHome(root)
		if migrationErr != nil {
			fmt.Fprintln(os.Stderr, "relay-app:", migrationErr)
		}
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
	if runtime.GOOS == "windows" {
		db.SetSecretStore(secretstore.New())
	}
	if err := workspacepkg.RecoverMigration(abs); err != nil {
		fmt.Fprintln(os.Stderr, "relay-app: recover workspace migration:", err)
		os.Exit(1)
	}
	if *migrateXray {
		if runtime.GOOS != "windows" {
			fmt.Fprintln(os.Stderr, "relay-app: Xray credential migration requires Windows Credential Manager")
			os.Exit(1)
		}
		if *xrayBackup == "" {
			fmt.Fprintln(os.Stderr, "relay-app: --xray-backup is required for credential migration")
			os.Exit(1)
		}
		if err := db.MigrateLegacyXrayCredentials(*xrayBackup); err != nil {
			fmt.Fprintln(os.Stderr, "relay-app: migrate Xray credentials:", err)
			os.Exit(1)
		}
		fmt.Println("Legacy SQLite Xray credentials cleared; database backup:", *xrayBackup)
		return
	}
	_, markerErr := os.Stat(filepath.Join(abs, "workspace.toml"))
	if markerErr != nil && !os.IsNotExist(markerErr) {
		fmt.Fprintln(os.Stderr, "relay-app:", markerErr)
		os.Exit(1)
	}
	if empty, err := db.Empty(); err == nil && empty && os.IsNotExist(markerErr) {
		_, _ = db.SeedFromDir(abs)
	}

	srv := &ui.Server{DB: db, Engine: engine.NewOptions(), WorkspaceRoot: abs}
	if err := srv.Prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "relay-app: open workspace:", err)
		os.Exit(1)
	}
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
