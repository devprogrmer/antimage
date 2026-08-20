package subscriptions

import "strings"

// Format represents a subscription config format.
type Format string

const (
	FormatV2Ray   Format = "v2ray"
	FormatClash   Format = "clash"
	FormatSingBox Format = "singbox"
)

// DetectFormat parses the User-Agent header and returns the appropriate format.
// Defaults to FormatV2Ray for maximum compatibility.
func DetectFormat(userAgent string) Format {
	ua := strings.ToLower(userAgent)

	// Clash detection (case-insensitive).
	if strings.Contains(ua, "clash") {
		return FormatClash
	}

	// sing-box and its variants (SFI = sing-box for iOS, SFA = sing-box for Android).
	if strings.Contains(ua, "sing-box") || strings.Contains(ua, "sfi") || strings.Contains(ua, "sfa") {
		return FormatSingBox
	}

	// v2rayN (Windows) and v2rayNG (Android) explicitly request v2ray format.
	if strings.Contains(ua, "v2rayn") || strings.Contains(ua, "v2rayng") {
		return FormatV2Ray
	}

	// Default to v2ray format for widest compatibility.
	return FormatV2Ray
}
