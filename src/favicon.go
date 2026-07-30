package main

import (
	"bytes"
	_ "embed"
	"net/http"
	"strconv"
	"time"
)

// The icon an MCP client shows next to the connector. Clients ask for it under
// several names depending on their age, so every one of them is served the
// same bytes.

//go:embed favicon.png
var faviconPNG []byte

var faviconModTime = time.Now()

func serveFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(faviconPNG)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "favicon.png", faviconModTime, bytes.NewReader(faviconPNG))
}
