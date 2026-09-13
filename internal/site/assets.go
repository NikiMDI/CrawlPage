package site

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets
var embeddedAssets embed.FS

func newAssetHandler() (http.Handler, error) {
	assetsRoot, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return nil, err
	}

	return http.StripPrefix(
		"/assets/",
		http.FileServer(http.FS(assetsRoot)),
	), nil
}
