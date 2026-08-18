package httpapi

import (
	"net/http"
	"os"

	"github.com/amyrm/antimage/internal/panel/webui"
)

// uiHandler serves the embedded SPA, or proxies to Vite when
// ANTIMAGE_DEV_PROXY is set.
func (d Deps) uiHandler() http.Handler {
	return webui.Handler(os.Getenv("ANTIMAGE_DEV_PROXY"))
}
