//go:build !gokrazy

package webui

import "net/http"

var gokrazyNavItems []navItem = nil

var gokrazyExternalNavItems = []navItem{}

func registerGoKrazyRoutes(mux *http.ServeMux) {

}
