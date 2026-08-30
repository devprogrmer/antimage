package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/skip2/go-qrcode"
)

// handleSubscriptionQR generates a QR code for a subscription URL.
// GET /api/v1/subscribe/{token}/qr
func (d Deps) handleSubscriptionQR(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		http.Error(w, "missing token", http.StatusNotFound)
		return
	}

	// Build subscription URL
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	subscriptionURL := fmt.Sprintf("%s://%s/api/v1/subscribe/%s", scheme, host, token)

	// Generate QR code (256x256 pixels, medium error correction)
	qr, err := qrcode.Encode(subscriptionURL, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
		return
	}

	// Return as PNG image.
	//
	// no-store, NOT "public, max-age=3600". The image encodes the subscription
	// token, which is the credential that grants access to this subject's
	// service -- a publicly cacheable response puts it in every intermediary
	// between the panel and the browser, and leaves it there for an hour after
	// the operator regenerates the link.
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(qr)
}

// qrRequest carries the text to encode.
type qrRequest struct {
	Text string `json:"text"`
}

// handleQRCode encodes arbitrary configuration text as a QR image.
//
// POST /api/v1/qr
//
// Authenticated, unlike the token QR above: the text is a client configuration
// URI carrying the subject's credential, so this is a credential-bearing
// response and is gated as one. A POST rather than a GET because the URI can
// be long, contains characters that do not survive a query string cleanly, and
// must not end up in an access log.
func (d Deps) handleQRCode(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermCredReveal, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	var req qrRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if req.Text == "" {
		WriteError(w, http.StatusBadRequest, "bad_request", "nothing to encode")
		return
	}
	// A QR code's capacity is finite, and a WireGuard profile is far past it.
	// Refusing with a reason beats returning an image that scans as garbage.
	if len(req.Text) > 2000 {
		WriteError(w, http.StatusUnprocessableEntity, "too_long",
			"this configuration is too long to encode as a QR code")
		return
	}

	png, err := qrcode.Encode(req.Text, qrcode.Medium, 256)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not generate QR code")
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}
