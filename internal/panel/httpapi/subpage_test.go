package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/shared/secrets"
)

func TestIsBrowserUA(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
		note string
	}{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36", true, "chrome"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1", true, "ios safari"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0", true, "firefox"},
		{"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Mobile Safari/537.36", true, "android chrome"},
		{"", false, "empty, e.g. Go's default client"},
		{"v2rayNG/1.8.0", false, "v2ray client"},
		{"okhttp/4.12.0", false, "android http library"},
		{"Mozilla/5.0 v2rayNG/1.8.0", false, "client identifying as mozilla too"},
		{"Mozilla/5.0 clash-verge/1.5", false, "clash dashboard webview"},
		{"Mozilla/5.0 sing-box/1.10", false, "sing-box"},
		{"Streisand/1.6.3 CFNetwork", false, "streisand, no mozilla token"},
		{"curl/8.5.0", false, "curl"},
	}
	for _, c := range cases {
		if got := isBrowserUA(c.ua); got != c.want {
			t.Errorf("isBrowserUA(%q) = %v, want %v (%s)", c.ua, got, c.want, c.note)
		}
	}
}

func TestPageLocale(t *testing.T) {
	cases := []struct {
		name    string
		lang    string
		accept  string
		wantTag string
		wantDir string
	}{
		{"explicit param wins", "fa", "en-US,en;q=0.9", "fa", "rtl"},
		{"accept prefix match", "", "fa-IR,fa;q=0.9,en;q=0.8", "fa", "rtl"},
		{"accept exact match", "", "ru-RU,ru;q=0.9", "ru", "ltr"},
		{"arabic is rtl", "ar", "", "ar", "rtl"},
		{"chinese", "", "zh-CN,zh;q=0.9", "zh-CN", "ltr"},
		{"unknown falls back to english", "fr", "de-DE,de;q=0.9", "en", "ltr"},
		{"empty everything is english", "", "", "en", "ltr"},
	}
	for _, c := range cases {
		got := pageLocale(c.lang, c.accept)
		if got.Tag != c.wantTag || got.Dir != c.wantDir {
			t.Errorf("%s: pageLocale(%q,%q) = %v, want %s/%s",
				c.name, c.lang, c.accept, got, c.wantTag, c.wantDir)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 KiB"},
		{2048, "2 KiB"},
		{5 << 20, "5 MiB"},
		{(3 << 30) + (500 << 20), "3.5 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildSubscriptionPageData(t *testing.T) {
	now := int64(1_700_000_000)
	expiry := now + 5*86400

	d := buildSubscriptionPageData("alice", 10<<30, 2<<30, expiry, now, "https://panel.example.com/api/v1/subscribe/tok")
	if !d.HasQuota || d.Percent != 20 {
		t.Errorf("percent = %d (hasQuota=%v), want 20", d.Percent, d.HasQuota)
	}
	if d.TotalHuman != "10.0 GiB" || d.UsedHuman != "2.0 GiB" || d.RemainHuman != "8.0 GiB" {
		t.Errorf("human values: %q / %q / %q", d.TotalHuman, d.UsedHuman, d.RemainHuman)
	}
	if !d.HasExpiry || d.DaysLeft != 5 {
		t.Errorf("days left = %d (hasExpiry=%v), want 5", d.DaysLeft, d.HasExpiry)
	}
	if d.QRURL != "https://panel.example.com/api/v1/subscribe/tok/qr" {
		t.Errorf("qr url = %q", d.QRURL)
	}
	if d.ClashURL != "https://panel.example.com/api/v1/subscribe/tok?format=clash" {
		t.Errorf("clash url = %q", d.ClashURL)
	}

	// Over-quota usage clamps at 100% rather than drawing a full circle plus
	// an overflow arc.
	d = buildSubscriptionPageData("alice", 10<<30, 40<<30, 0, now, "https://x/api/v1/subscribe/tok")
	if d.Percent != 100 {
		t.Errorf("percent = %d, want 100 after clamp", d.Percent)
	}

	// Unlimited: no quota ring, no expiry block.
	d = buildSubscriptionPageData("alice", 0, 1<<20, 0, now, "https://x/api/v1/subscribe/tok")
	if d.HasQuota || d.HasExpiry {
		t.Errorf("unlimited subject rendered as limited: %+v", d)
	}
	if d.UsedHuman != "1 MiB" {
		t.Errorf("used = %q, want 1 MiB", d.UsedHuman)
	}
}

// seedPageSubject inserts a subject with a token, quota and expiry, plus the
// node/service/credential chain the subscription renderer needs to produce a
// non-empty payload. It is the ValidToken seeding plus the quota columns.
// An expiry of 0 means "did not set one", not "expired at the epoch", so it
// is stored as a month out.
func seedPageSubject(t *testing.T, env *testEnv, name, token string, quotaBytes int64, expires int64) {
	t.Helper()
	if expires == 0 {
		expires = time.Now().Add(30 * 24 * time.Hour).Unix()
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	env.box = box

	ctx := context.Background()
	var subjectID int64
	err = env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subjects (name, enabled, subscription_token, quota_bytes, quota_used_bytes, expires_at, created_at)
			VALUES (?, 1, ?, ?, ?, ?, ?)`,
			name, token, quotaBytes, 2<<30, expires, time.Now().Unix())
		if err != nil {
			return err
		}
		subjectID, _ = res.LastInsertId()

		res, err = tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, created_at) VALUES (?, ?, 'online', ?)`,
			"test-node", "node.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ := res.LastInsertId()

		res, err = tx.ExecContext(ctx, `
			INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			VALUES (?, 'xray', '{"protocol":"vless","port":443}', 1, ?)`, nodeID, time.Now().Unix())
		if err != nil {
			return err
		}
		serviceID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO subject_services (subject_id, service_id) VALUES (?, ?)`, subjectID, serviceID); err != nil {
			return err
		}

		uuidEnc, _ := box.Seal([]byte("11111111-2222-3333-4444-555555555555"))
		_, err = tx.ExecContext(ctx, `
			INSERT INTO subject_credentials (subject_id, kind, value_enc, created_at) VALUES (?, 'uuid', ?, ?)`,
			subjectID, uuidEnc, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}
}

func doSubscribeWithUA(t *testing.T, env *testEnv, path, ua string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	req.Header.Set("User-Agent", ua)
	req.Host = "panel.local"
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec
}

func TestSubscribeUserinfoHeader(t *testing.T) {
	env := newTestEnv(t)
	expiry := int64(1_700_000_000) + 30*86400
	seedPageSubject(t, env, "quota@example.com", "quota-token", 10<<30, expiry)

	// A proxy client UA: the payload must still be the base64 config, and the
	// standard usage header must ride along so the client can render usage
	// and expiry in-app without another request.
	rec := doSubscribeWithUA(t, env, "/api/v1/subscribe/quota-token", "v2rayNG/1.8.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	want := "upload=0; download=2147483648; total=10737418240; expire=" + itoa64(expiry)
	if got := rec.Header().Get("Subscription-Userinfo"); got != want {
		t.Errorf("Subscription-Userinfo = %q, want %q", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") && !strings.Contains(ct, "application/octet-stream") {
		// The renderer decides the exact type; it must not be the HTML page.
		t.Errorf("client UA content-type = %q", ct)
	}
	if decoded, err := base64.StdEncoding.DecodeString(rec.Body.String()); err != nil || !strings.Contains(string(decoded), "vless://") {
		t.Errorf("client UA body is not a v2ray payload")
	}
}

func TestSubscribeBrowserGetsInfoPage(t *testing.T) {
	env := newTestEnv(t)
	seedPageSubject(t, env, "browser@example.com", "browser-token", 10<<30, 0)

	rec := doSubscribeWithUA(t, env, "/api/v1/subscribe/browser-token",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("browser UA should get HTML, got %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<html lang="en" dir="ltr">`,
		"browser@example.com",
		"http://panel.local/api/v1/subscribe/browser-token",
		"/qr",
		"Your subscription",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The userinfo header is on the page response too: some panels' users
	// bookmark the page, and browser extensions replay the headers.
	if got := rec.Header().Get("Subscription-Userinfo"); !strings.Contains(got, "download=2147483648") {
		t.Errorf("page response missing userinfo header, got %q", got)
	}
}

func TestSubscribeInfoPageLanguage(t *testing.T) {
	env := newTestEnv(t)
	seedPageSubject(t, env, "fa@example.com", "fa-token", 0, 0)

	// ?lang= wins, Farsi renders right-to-left.
	rec := doSubscribeWithUA(t, env, "/api/v1/subscribe/fa-token?lang=fa", "Mozilla/5.0 Chrome/126.0")
	if !strings.Contains(rec.Body.String(), `dir="rtl"`) {
		t.Errorf("fa page should be rtl, body starts: %.200s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "لینک اشتراک") {
		t.Errorf("fa page should use the farsi catalogue")
	}

	// Accept-Language works when the query does not override it.
	rec = doSubscribeWithUA(t, env, "/api/v1/subscribe/fa-token", "Mozilla/5.0 Chrome/126.0")
	_ = rec
	req := httptest.NewRequest(http.MethodGet, "/api/v1/subscribe/fa-token", strings.NewReader(""))
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/126.0")
	req.Header.Set("Accept-Language", "fa-IR,fa;q=0.9,en;q=0.8")
	rec2 := httptest.NewRecorder()
	env.handler.ServeHTTP(rec2, req)
	if !strings.Contains(rec2.Body.String(), `lang="fa" dir="rtl"`) {
		t.Errorf("Accept-Language fa should select the farsi page")
	}
}

func TestSubscribeFormatQueryOverridesUA(t *testing.T) {
	env := newTestEnv(t)
	seedPageSubject(t, env, "fmt@example.com", "fmt-token", 0, 0)

	// An explicit format is the operator saying what they want; a browser
	// asking for clash gets clash, and so does any client asking for html.
	rec := doSubscribeWithUA(t, env, "/api/v1/subscribe/fmt-token?format=clash", "Mozilla/5.0 Chrome/126.0")
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Errorf("format=clash must override the browser page, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "proxies") {
		t.Errorf("clash payload should be yaml, got %.200s", rec.Body.String())
	}

	rec = doSubscribeWithUA(t, env, "/api/v1/subscribe/fmt-token?format=html", "v2rayNG/1.8.0")
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("format=html must force the page even for a client UA")
	}
}

func TestSubscribeInfoPageEscapesName(t *testing.T) {
	env := newTestEnv(t)
	seedPageSubject(t, env, `<script>alert(1)</script>`, "xss-token", 0, 0)

	rec := doSubscribeWithUA(t, env, "/api/v1/subscribe/xss-token", "Mozilla/5.0 Chrome/126.0")
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("subject name was injected unescaped into a public page")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected the escaped name in the page")
	}
}
