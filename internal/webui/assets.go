//go:build !builtinassets

package webui

import "net/http"

func fixAssetURL(url string) string {
	return url
}

var assetsHandler = http.NotFoundHandler()
