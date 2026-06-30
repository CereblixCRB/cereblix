// Command cereblix-desktop is the Cereblix desktop wallet: a Wails v2 app whose Go
// backend wraps the coin's own packages (cereblix/core for crypto/signing/types,
// cereblix/node for the optional embedded full node) and a vanilla HTML/CSS/JS
// frontend served from the embedded filesystem. Keys and signing stay 100% local.
package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// assets embeds the entire vanilla frontend (index.html + css/js/assets). The
// `all:` prefix also includes files that start with `_` or `.`.
//
//go:embed all:frontend
var assets embed.FS

func main() {
	app := NewApp()

	// Serve the embedded frontend rooted at the `frontend/` directory.
	sub, err := fs.Sub(assets, "frontend")
	if err != nil {
		log.Fatalf("embed frontend: %v", err)
	}

	err = wails.Run(&options.App{
		Title:     "Cereblix Wallet",
		Width:     1180,
		Height:    820,
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: sub,
		},
		OnStartup: app.startup,
		Bind: []any{
			app,
		},
		// One running wallet per machine: a second launch focuses the existing
		// window instead of opening another (and racing on wallet.json).
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.cereblix.desktop.wallet",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				if ctx := app.ctx; ctx != nil {
					runtime.WindowUnminimise(ctx)
					runtime.Show(ctx)
				}
			},
		},
	})
	if err != nil {
		log.Fatalf("wails run: %v", err)
	}
}
