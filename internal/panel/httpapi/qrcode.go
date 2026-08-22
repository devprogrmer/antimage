package httpapi

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
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

	// Return as PNG image
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600") // Cache for 1 hour
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(qr)
}
