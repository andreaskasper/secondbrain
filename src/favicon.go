package main

import (
	"bytes"
	_ "embed"
	"net/http"
	"strconv"
	"time"
)

// The icon an MCP client shows next to the connector. Clients ask for it under
// several names depending on their age, so every one of them gets the same
// bytes.
//
// It is vector rather than raster on purpose. A PNG in a source tree is an
// opaque blob that no review can read and no diff can describe, and this one
// would be the only binary in the repository. An SVG is text: it can be
// reviewed, it carries the site's own gradient as a definition rather than as
// baked-in pixels, and it is sharp at every size a client might ask for.

//go:embed favicon.svg
var faviconSVG []byte

var faviconModTime = time.Now()

func serveFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(faviconSVG)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "favicon.svg", faviconModTime, bytes.NewReader(faviconSVG))
}
