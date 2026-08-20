package subscriptions

import "testing"

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		userAgent string
		want      Format
	}{
		// Clash variants
		{"Clash/1.0", FormatClash},
		{"clash for windows", FormatClash},
		{"ClashX/1.2.3", FormatClash},
		{"CLASH", FormatClash},

		// sing-box variants
		{"sing-box/1.3.0", FormatSingBox},
		{"SFI/1.0", FormatSingBox},
		{"SFA/2.1", FormatSingBox},
		{"Sing-Box", FormatSingBox},

		// v2ray variants
		{"v2rayN/5.0", FormatV2Ray},
		{"v2rayNG/1.8.0", FormatV2Ray},
		{"V2RayN", FormatV2Ray},
		{"v2rayng", FormatV2Ray},

		// Default fallback
		{"curl/7.68.0", FormatV2Ray},
		{"Mozilla/5.0", FormatV2Ray},
		{"wget", FormatV2Ray},
		{"", FormatV2Ray},
		{"unknown-client", FormatV2Ray},
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			got := DetectFormat(tt.userAgent)
			if got != tt.want {
				t.Errorf("DetectFormat(%q) = %q, want %q", tt.userAgent, got, tt.want)
			}
		})
	}
}

func TestDetectFormat_CaseInsensitive(t *testing.T) {
	// Verify case-insensitive matching.
	cases := []string{
		"clash", "CLASH", "Clash", "ClAsH",
		"v2rayn", "V2RAYN", "V2RayN",
		"sing-box", "SING-BOX", "Sing-Box",
	}

	for _, ua := range cases {
		format := DetectFormat(ua)
		if format == "" {
			t.Errorf("DetectFormat(%q) returned empty format", ua)
		}
	}
}

func TestDetectFormat_Priority(t *testing.T) {
	// If UA contains multiple keywords, first match wins.
	// Current priority: Clash > sing-box > v2ray > default.

	// "clash" appears first, should match Clash.
	if got := DetectFormat("clash-with-v2ray"); got != FormatClash {
		t.Errorf("expected Clash, got %q", got)
	}

	// "sing-box" appears before "v2ray".
	if got := DetectFormat("sing-box v2ray hybrid"); got != FormatSingBox {
		t.Errorf("expected SingBox, got %q", got)
	}
}
