//go:build !gokrazy

package webui

import "net/http"

var gokrazyNavItems []navItem = nil

func registerGoKrazyRoutes(mux *http.ServeMux) {

}
