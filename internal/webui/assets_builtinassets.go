//go:build builtinassets

package webui

import (
	"embed"
	"net/http"
)

//go:embed assets
var content embed.FS

func fixAssetURL(url string) string {
	if assetPath, ok := assetsMap[url]; ok {
		if _, err := content.Open(assetPath); err == nil {
			return assetPath
		}
	}

	return url
}

var assetsHandler = http.FileServer(http.FS(content))
