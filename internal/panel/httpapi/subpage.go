package httpapi

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strings"
	"time"
)

// The subscription endpoint serves two audiences: proxy clients, which want
// raw config payloads, and people, who paste the subscription link into a
// browser and until now were handed a wall of base64. Every panel in this
// space serves those people an information page instead -- usage, expiry,
// the links in every format, a QR code -- so this file renders one.
//
// Constraints the page is written under:
//
//   - Self-contained. One HTML document, inline CSS, one inline script, no
//     external font/CDN/script requests. A subscription link is opened on
//     censored networks; the page must render with nothing but itself.
//   - Bilingual by Accept-Language, including right-to-left for Farsi and
//     Arabic, which is a majority audience for this kind of panel.
//   - The only sub-request is the QR image, served by this same handler from
//     the adjacent /qr route, so no third party learns the token.
//   - html/template escaping stays on: the subject name is operator-entered
//     and must not become markup on a public, unauthenticated page.

// proxyClientTokens are User-Agent fragments of known subscription *clients*.
// They all also contain "Mozilla" (or their WebView does), so browser
// detection alone is not enough -- the client has to be excluded by name or
// v2rayNG's in-app browser and every Clash dashboard would get the page
// instead of the payload.
var proxyClientTokens = []string{
	"v2ray", "clash", "sing-box", "singbox", "sfi", "sfa", "sfm",
	"shadowrocket", "streisand", "hiddify", "havoc", "nekobox", "nekoray",
	"loon", "surge", "quantumult", "stash", "karing", "husi", "v2box",
	"shadowsocks", "ssr", "geph", "brook", "outline", "wireproxy", "tuic",
}

// isBrowserUA reports whether the User-Agent looks like an interactive
// browser rather than a proxy client or a plain HTTP library. Browsers all
// identify as Mozilla descendants; HTTP libraries (okhttp, Go's http client,
// curl) do not, and they are exactly the callers that must keep receiving
// machine payloads.
func isBrowserUA(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	if !strings.Contains(ua, "mozilla") {
		return false
	}
	for _, token := range proxyClientTokens {
		if strings.Contains(ua, token) {
			return false
		}
	}
	return true
}

// pageLang is a BCP-47 tag plus the direction its text renders in.
type pageLang struct {
	Tag string
	Dir string
}

// pageLocale picks the page language from an explicit ?lang= override, then
// Accept-Language prefix matching, then English. Only languages the page is
// actually translated into are offered; anything else falls back rather than
// rendering a half-translated page.
func pageLocale(langParam string, acceptLanguage string) pageLang {
	rtl := pageLang{Tag: "fa", Dir: "rtl"}
	candidates := []pageLang{
		{Tag: "en", Dir: "ltr"},
		rtl,
		{Tag: "ru", Dir: "ltr"},
		{Tag: "ar", Dir: "rtl"},
		{Tag: "zh-CN", Dir: "ltr"},
	}
	match := func(tag string) (pageLang, bool) {
		for _, c := range candidates {
			if strings.EqualFold(c.Tag, tag) {
				return c, true
			}
		}
		return pageLang{}, false
	}
	if l := strings.TrimSpace(langParam); l != "" {
		if c, ok := match(l); ok {
			return c
		}
	}
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if tag == "" {
			continue
		}
		// The full tag first ("zh-CN" is a catalogue entry; "zh" is not),
		// then the language-only prefix, which covers "en-US" -> "en".
		if c, ok := match(tag); ok {
			return c
		}
		if i := strings.IndexByte(tag, '-'); i > 0 {
			if c, ok := match(tag[:i]); ok {
				return c
			}
		}
	}
	return candidates[0]
}

// pageStrings is everything the template interpolates per language. A struct
// rather than a map so a missing translation is a compile-time error list,
// not a blank button shipped to users.
type pageStrings struct {
	Title        string
	StatusActive string
	Used         string
	Limit        string
	Remaining    string
	Unlimited    string
	ExpiresOn    string
	DaysLeft     string
	NoExpiry     string
	YourLink     string
	YourLinkHint string
	Copy         string
	Copied       string
	FormatAll    string
	FormatClash  string
	FormatSing   string
	ScanQR       string
	ClientsTitle string
	ClientsHint  string
	PoweredBy    string
}

var pageCatalogue = map[string]pageStrings{
	"en": {
		Title:        "Your subscription",
		StatusActive: "Active",
		Used:         "Used",
		Limit:        "Limit",
		Remaining:    "Remaining",
		Unlimited:    "Unlimited",
		ExpiresOn:    "Expires on",
		DaysLeft:     "days left",
		NoExpiry:     "No expiry date",
		YourLink:     "Subscription link",
		YourLinkHint: "Add this link to any client app. Usage and expiry update automatically.",
		Copy:         "Copy",
		Copied:       "Copied!",
		FormatAll:    "All clients (V2Ray)",
		FormatClash:  "Clash / Clash Meta",
		FormatSing:   "sing-box",
		ScanQR:       "Scan to add this subscription",
		ClientsTitle: "Recommended apps",
		ClientsHint:  "Install one of these, then add the subscription link.",
		PoweredBy:    "Powered by antimage",
	},
	"fa": {
		Title:        "اشتراک شما",
		StatusActive: "فعال",
		Used:         "مصرف‌شده",
		Limit:        "حجم کل",
		Remaining:    "باقی‌مانده",
		Unlimited:    "نامحدود",
		ExpiresOn:    "تاریخ انقضا",
		DaysLeft:     "روز باقی‌مانده",
		NoExpiry:     "بدون تاریخ انقضا",
		YourLink:     "لینک اشتراک",
		YourLinkHint: "این لینک را در هر برنامهٔ کلاینت اضافه کنید. مصرف و انقضا به‌صورت خودکار به‌روز می‌شود.",
		Copy:         "کپی",
		Copied:       "کپی شد!",
		FormatAll:    "همه کلاینت‌ها (V2Ray)",
		FormatClash:  "Clash / Clash Meta",
		FormatSing:   "sing-box",
		ScanQR:       "برای افزودن اشتراک، این کد را اسکن کنید",
		ClientsTitle: "برنامه‌های پیشنهادی",
		ClientsHint:  "یکی از این برنامه‌ها را نصب کنید و لینک اشتراک را اضافه کنید.",
		PoweredBy:    "قدرت‌گرفته از antimage",
	},
	"ru": {
		Title:        "Ваша подписка",
		StatusActive: "Активна",
		Used:         "Использовано",
		Limit:        "Лимит",
		Remaining:    "Осталось",
		Unlimited:    "Безлимит",
		ExpiresOn:    "Действует до",
		DaysLeft:     "дней осталось",
		NoExpiry:     "Без срока действия",
		YourLink:     "Ссылка на подписку",
		YourLinkHint: "Добавьте эту ссылку в любое приложение-клиент. Трафик и срок обновляются автоматически.",
		Copy:         "Копировать",
		Copied:       "Скопировано!",
		FormatAll:    "Все клиенты (V2Ray)",
		FormatClash:  "Clash / Clash Meta",
		FormatSing:   "sing-box",
		ScanQR:       "Отсканируйте, чтобы добавить подписку",
		ClientsTitle: "Рекомендуемые приложения",
		ClientsHint:  "Установите одно из них и добавьте ссылку на подписку.",
		PoweredBy:    "Работает на antimage",
	},
	"ar": {
		Title:        "اشتراكك",
		StatusActive: "نشط",
		Used:         "المستخدم",
		Limit:        "الحد",
		Remaining:    "المتبقي",
		Unlimited:    "غير محدود",
		ExpiresOn:    "تاريخ الانتهاء",
		DaysLeft:     "يومًا متبقيًا",
		NoExpiry:     "بدون تاريخ انتهاء",
		YourLink:     "رابط الاشتراك",
		YourLinkHint: "أضف هذا الرابط إلى أي تطبيق عميل. يُحدَّث الاستهلاك والانتهاء تلقائيًا.",
		Copy:         "نسخ",
		Copied:       "تم النسخ!",
		FormatAll:    "جميع العملاء (V2Ray)",
		FormatClash:  "Clash / Clash Meta",
		FormatSing:   "sing-box",
		ScanQR:       "امسح هذا الرمز لإضافة الاشتراك",
		ClientsTitle: "تطبيقات موصى بها",
		ClientsHint:  "ثبِّت أحد هذه التطبيقات ثم أضف رابط الاشتراك.",
		PoweredBy:    "مدعوم من antimage",
	},
	"zh-CN": {
		Title:        "你的订阅",
		StatusActive: "生效中",
		Used:         "已用",
		Limit:        "总额度",
		Remaining:    "剩余",
		Unlimited:    "不限量",
		ExpiresOn:    "到期时间",
		DaysLeft:     "天后到期",
		NoExpiry:     "永不过期",
		YourLink:     "订阅链接",
		YourLinkHint: "将此链接添加到任意客户端，用量与到期时间会自动更新。",
		Copy:         "复制",
		Copied:       "已复制！",
		FormatAll:    "通用（V2Ray）",
		FormatClash:  "Clash / Clash Meta",
		FormatSing:   "sing-box",
		ScanQR:       "扫码添加此订阅",
		ClientsTitle: "推荐应用",
		ClientsHint:  "安装其中之一，然后添加订阅链接。",
		PoweredBy:    "由 antimage 提供支持",
	},
}

// subscriptionPageData is the full view model for the page.
type subscriptionPageData struct {
	Lang     pageLang
	Str      pageStrings
	Name     string
	PageURL  string
	QRURL    string
	ClashURL string
	SingURL  string
	// Quota. Total of 0 means unlimited; the ring is hidden rather than
	// rendered full, because "100% of infinity" is not a number.
	TotalBytes        int64
	UsedBytes         int64
	Percent           int
	UsedHuman         string
	TotalHuman        string
	RemainHuman       string
	HasQuota          bool
	RingCircumference float64
	RingOffset        float64
	ExpiresAt         int64
	HasExpiry         bool
	ExpiresHuman      string
	DaysLeft          int
	// NowISO grounds the rendered timestamp for tests without a clock read
	// from inside the template.
	NowISO string
}

// humanBytes renders a byte count the way every client app does: GB when the
// value is at least one GB, MB below that. Base-2, like the quota itself.
func humanBytes(b int64) string {
	const gib = 1 << 30
	const mib = 1 << 20
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	case b >= mib:
		return fmt.Sprintf("%d MiB", b/mib)
	default:
		return fmt.Sprintf("%d KiB", b/1024)
	}
}

// subscriptionPageTemplate is parsed once. A parse failure panaps at first
// render rather than being silently tolerated: a broken template must fail
// tests, never users.
var subscriptionPageTemplate = template.Must(template.New("sub").Parse(subscriptionPageHTML))

const subscriptionPageHTML = `<!DOCTYPE html>
<html lang="{{.Lang.Tag}}" dir="{{.Lang.Dir}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Str.Title}}</title>
<style>
:root {
  --bg: #f4f5f9; --card: #ffffff; --text: #16181d; --muted: #62697a;
  --line: #e3e6ee; --accent: #4f6df5; --accent-soft: #eef1fe;
  --ok: #14915f; --ok-soft: #e5f6ee; --track: #e8eaf2;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0e1015; --card: #161921; --text: #eceef4; --muted: #9aa2b5;
    --line: #262b38; --accent: #7d92ff; --accent-soft: #1c2133;
    --ok: #3dd68c; --ok-soft: #12291e; --track: #222736;
  }
}
* { box-sizing: border-box; margin: 0; }
body {
  background: var(--bg); color: var(--text);
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, "Noto Naskh Arabic", "Noto Sans", sans-serif;
  min-height: 100vh; display: flex; flex-direction: column; align-items: center;
  padding: 24px 16px 40px; line-height: 1.6;
}
.card {
  background: var(--card); border: 1px solid var(--line); border-radius: 16px;
  width: 100%; max-width: 560px; padding: 24px; margin-bottom: 16px;
}
.head { display: flex; align-items: center; gap: 12px; margin-bottom: 20px; flex-wrap: wrap; }
.avatar {
  width: 44px; height: 44px; border-radius: 12px; background: var(--accent-soft);
  color: var(--accent); display: flex; align-items: center; justify-content: center;
  font-size: 20px; font-weight: 700; flex: none;
}
.name { font-size: 17px; font-weight: 650; word-break: break-all; }
.chip {
  background: var(--ok-soft); color: var(--ok); border-radius: 999px;
  padding: 2px 12px; font-size: 12.5px; font-weight: 600;
}
.usage { display: flex; align-items: center; gap: 20px; }
.ring { flex: none; width: 108px; height: 108px; }
.ring text { font-size: 19px; font-weight: 700; fill: var(--text); }
.stats { display: flex; flex-direction: column; gap: 8px; min-width: 0; flex: 1; }
.stat { display: flex; justify-content: space-between; gap: 12px; font-size: 14px; }
.stat b { font-weight: 650; font-variant-numeric: tabular-nums; }
.stat span { color: var(--muted); }
.bar { height: 8px; border-radius: 999px; background: var(--track); overflow: hidden; margin-top: 4px; }
.bar > i { display: block; height: 100%; background: var(--accent); border-radius: 999px; }
h2 { font-size: 14px; font-weight: 650; margin-bottom: 10px; color: var(--muted); text-transform: uppercase; letter-spacing: .04em; }
.linkbox {
  display: flex; gap: 8px; align-items: stretch; margin-bottom: 12px;
}
.linkbox input {
  flex: 1; min-width: 0; background: var(--bg); color: var(--text);
  border: 1px solid var(--line); border-radius: 10px; padding: 10px 12px;
  font-size: 13px; font-family: ui-monospace, monospace; direction: ltr; text-align: left;
}
.btn {
  background: var(--accent); color: #fff; border: 0; border-radius: 10px;
  padding: 10px 16px; font-size: 13.5px; font-weight: 600; cursor: pointer; flex: none;
}
.btn:hover { filter: brightness(1.08); }
.fmts { display: grid; grid-template-columns: 1fr; gap: 8px; }
.fmt {
  display: flex; justify-content: space-between; align-items: center; gap: 8px;
  border: 1px solid var(--line); border-radius: 10px; padding: 10px 12px; font-size: 13.5px;
}
.fmt span { color: var(--muted); }
.fmt button {
  background: var(--accent-soft); color: var(--accent); border: 0; border-radius: 8px;
  padding: 6px 12px; font-size: 12.5px; font-weight: 650; cursor: pointer;
}
.qr { text-align: center; }
.qr img { width: 190px; height: 190px; border-radius: 12px; border: 1px solid var(--line); background: #fff; padding: 8px; }
.qr p { color: var(--muted); font-size: 13px; margin-top: 8px; }
.apps { display: flex; flex-wrap: wrap; gap: 8px; }
.apps a {
  border: 1px solid var(--line); border-radius: 999px; padding: 6px 14px;
  font-size: 13px; color: var(--accent); text-decoration: none;
}
.apps a:hover { background: var(--accent-soft); }
footer { color: var(--muted); font-size: 12.5px; margin-top: 8px; }
</style>
</head>
<body>

<section class="card">
  <div class="head">
    <div class="avatar">&#9998;</div>
    <div style="min-width:0">
      <div class="name">{{.Name}}</div>
    </div>
    <span class="chip">{{.Str.StatusActive}}</span>
  </div>

  {{if .HasQuota}}
  <div class="usage">
    <svg class="ring" viewBox="0 0 120 120" role="img" aria-label="{{.Percent}}%">
      <circle cx="60" cy="60" r="52" fill="none" stroke="var(--track)" stroke-width="10"/>
      <circle cx="60" cy="60" r="52" fill="none" stroke="var(--accent)" stroke-width="10"
        stroke-linecap="round" stroke-dasharray="{{.RingCircumference}} {{.RingCircumference}}"
        stroke-dashoffset="{{.RingOffset}}" transform="rotate(-90 60 60)"/>
      <text x="60" y="66" text-anchor="middle">{{.Percent}}%</text>
    </svg>
    <div class="stats">
      <div class="stat"><span>{{.Str.Used}}</span><b>{{.UsedHuman}}</b></div>
      <div class="stat"><span>{{.Str.Limit}}</span><b>{{.TotalHuman}}</b></div>
      <div class="stat"><span>{{.Str.Remaining}}</span><b>{{.RemainHuman}}</b></div>
    </div>
  </div>
  {{else}}
  <div class="usage">
    <div class="avatar" style="width:108px;height:108px;border-radius:16px;font-size:34px">&#8734;</div>
    <div class="stats">
      <div class="stat"><span>{{.Str.Limit}}</span><b>{{.Str.Unlimited}}</b></div>
      <div class="stat"><span>{{.Str.Used}}</span><b>{{.UsedHuman}}</b></div>
    </div>
  </div>
  {{end}}

  <div style="margin-top:18px;font-size:14px">
    {{if .HasExpiry}}
    <div class="stat"><span>{{.Str.ExpiresOn}}</span><b>{{.ExpiresHuman}}</b></div>
    <div class="stat"><span></span><b style="color:var(--muted)">{{.DaysLeft}} {{.Str.DaysLeft}}</b></div>
    {{else}}
    <div class="stat"><span>{{.Str.ExpiresOn}}</span><b>{{.Str.NoExpiry}}</b></div>
    {{end}}
  </div>
</section>

<section class="card">
  <h2>{{.Str.YourLink}}</h2>
  <p style="color:var(--muted);font-size:13px;margin-bottom:12px">{{.Str.YourLinkHint}}</p>
  <div class="linkbox">
    <input id="sub" readonly value="{{.PageURL}}" aria-label="{{.Str.YourLink}}" onfocus="this.select()">
    <button class="btn" data-copy="sub" data-default="{{.Str.Copy}}" data-copied="{{.Str.Copied}}">{{.Str.Copy}}</button>
  </div>
  <div class="fmts">
    <div class="fmt"><div>{{.Str.FormatAll}}</div><button data-copy="fmt-v2ray" data-default="{{.Str.Copy}}" data-copied="{{.Str.Copied}}">{{.Str.Copy}}</button></div>
    <div class="fmt"><div>{{.Str.FormatClash}}</div><button data-copy="fmt-clash" data-default="{{.Str.Copy}}" data-copied="{{.Str.Copied}}">{{.Str.Copy}}</button></div>
    <div class="fmt"><div>{{.Str.FormatSing}}</div><button data-copy="fmt-singbox" data-default="{{.Str.Copy}}" data-copied="{{.Str.Copied}}">{{.Str.Copy}}</button></div>
  </div>
  <div hidden>
    <input id="fmt-v2ray" readonly value="{{.PageURL}}">
    <input id="fmt-clash" readonly value="{{.ClashURL}}">
    <input id="fmt-singbox" readonly value="{{.SingURL}}">
  </div>
</section>

<section class="card qr">
  <h2 style="text-align:start">{{.Str.ScanQR}}</h2>
  <img src="{{.QRURL}}" alt="{{.Str.ScanQR}}" width="190" height="190">
</section>

<section class="card">
  <h2>{{.Str.ClientsTitle}}</h2>
  <p style="color:var(--muted);font-size:13px;margin-bottom:12px">{{.Str.ClientsHint}}</p>
  <div class="apps">
    <a href="https://github.com/2dust/v2rayNG/releases/latest" rel="noopener">v2rayNG <small>(Android)</small></a>
    <a href="https://apps.apple.com/app/streisand/id6450534064" rel="noopener">Streisand <small>(iOS)</small></a>
    <a href="https://github.com/hiddify/hiddify-app/releases/latest" rel="noopener">Hiddify <small>(All)</small></a>
    <a href="https://github.com/MatsuriDayo/NekoBoxForAndroid/releases/latest" rel="noopener">NekoBox <small>(Android)</small></a>
    <a href="https://github.com/SagerNet/sing-box/releases/latest" rel="noopener">sing-box <small>(CLI)</small></a>
  </div>
</section>

<footer>{{.Str.PoweredBy}} &middot; <time>{{.NowISO}}</time></footer>

<script>
// Copy buttons share one handler: read the target input, write it to the
// clipboard, flash the label. No network, no dependencies, no tracking.
document.addEventListener("click", function (e) {
  var b = e.target.closest("[data-copy]");
  if (!b) return;
  var src = document.getElementById(b.getAttribute("data-copy"));
  if (!src) return;
  var done = function () {
    b.textContent = b.getAttribute("data-copied");
    setTimeout(function () { b.textContent = b.getAttribute("data-default"); }, 1600);
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(src.value).then(done, function () { src.select(); done(); });
  } else { src.select(); done(); }
});
</script>
</body>
</html>`

// renderSubscriptionPage writes the human-facing page. It never errors after
// headers are written; template execution failures (which only a bug can
// cause, since Parse ran at init) truncate the response rather than panic on
// a public endpoint.
func renderSubscriptionPage(w http.ResponseWriter, r *http.Request, data subscriptionPageData) {
	lang := pageLocale(r.URL.Query().Get("lang"), r.Header.Get("Accept-Language"))
	data.Lang = lang
	data.Str = pageCatalogue[lang.Tag]

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	_ = subscriptionPageTemplate.Execute(w, data)
}

// buildSubscriptionPageData assembles the view model from the subject row
// the subscribe handler already loaded. Ring geometry is precomputed here so
// the template stays arithmetic-free.
func buildSubscriptionPageData(name string, quotaBytes, quotaUsed, expiresAt, now int64, pageURL string) subscriptionPageData {
	data := subscriptionPageData{
		Name:       name,
		PageURL:    pageURL,
		QRURL:      strings.TrimSuffix(pageURL, "/") + "/qr",
		ClashURL:   pageURL + "?format=clash",
		SingURL:    pageURL + "?format=singbox",
		TotalBytes: quotaBytes,
		UsedBytes:  quotaUsed,
		ExpiresAt:  expiresAt,
		NowISO:     time.Unix(now, 0).UTC().Format(time.RFC3339),
	}
	if quotaBytes > 0 {
		data.HasQuota = true
		data.TotalHuman = humanBytes(quotaBytes)
		data.UsedHuman = humanBytes(quotaUsed)
		remain := quotaBytes - quotaUsed
		if remain < 0 {
			remain = 0
		}
		data.RemainHuman = humanBytes(remain)
		data.Percent = int(math.Round(float64(quotaUsed) / float64(quotaBytes) * 100))
		if data.Percent > 100 {
			data.Percent = 100
		}
		const circumference = 2 * math.Pi * 52
		data.RingCircumference = circumference
		data.RingOffset = circumference * (1 - float64(data.Percent)/100)
	} else {
		data.UsedHuman = humanBytes(quotaUsed)
	}
	if expiresAt > 0 {
		data.HasExpiry = true
		data.ExpiresHuman = time.Unix(expiresAt, 0).UTC().Format("2006-01-02 15:04 UTC")
		data.DaysLeft = int(time.Unix(expiresAt, 0).Sub(time.Unix(now, 0)).Hours() / 24)
		if data.DaysLeft < 0 {
			data.DaysLeft = 0
		}
	}
	return data
}
