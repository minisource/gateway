// Package respond centralizes gateway error responses. Requests that come
// from a browser (Accept: text/html or Sec-Fetch-Mode: navigate) receive a
// friendly, self-contained HTML page instead of a raw JSON error, so users are
// not confused when an upstream service is deploying or temporarily down.
// API clients keep the exact JSON payload the caller provides.
//
// All user-facing messages are translated via go-common i18n. The language is
// read from the request (lang query parameter or Accept-Language header) with
// English as the fallback; Persian (fa) and English (en) are supported.
package respond

import (
	"html"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/go-common/i18n"
)

// T translates an i18n key using the request's language (query ?lang= or
// Accept-Language header). Returns the key itself when no translation exists.
func T(c *fiber.Ctx, key string, params ...map[string]interface{}) string {
	return i18n.T(c, key, params...)
}

// lang returns the normalized language code ("en" or "fa") for the request.
func lang(c *fiber.Ctx) string {
	return i18n.GetTranslator().GetLangFromContext(c)
}

// IsBrowserRequest reports whether the client expects an HTML document rather
// than a structured API response. Browsers navigating send Accept: text/html;
// API clients (fetch/axios) send application/json or */*. The Sec-Fetch-Mode
// header is the most reliable signal in modern browsers.
func IsBrowserRequest(c *fiber.Ctx) bool {
	if mode := c.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate" || mode == "nested-navigate"
	}
	accept := c.Get(fiber.HeaderAccept)
	if accept == "" {
		return false
	}
	// Accept may be a comma-separated list; the browser always lists text/html.
	for _, part := range strings.Split(accept, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if strings.Contains(part, "text/html") {
			return true
		}
	}
	return false
}

// WriteError responds with the given JSON payload to API clients, or with a
// friendly HTML page to browsers. The JSON payload is passed through verbatim,
// so per-caller error shapes are preserved exactly. Callers should pass an
// already-translated message (see T) so both API clients and the HTML detail
// line are localized.
func WriteError(c *fiber.Ctx, status int, code, message string, jsonPayload fiber.Map) error {
	if !IsBrowserRequest(c) {
		return c.Status(status).JSON(jsonPayload)
	}

	requestLang := lang(c)
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	// Let browsers retry automatically once the service comes back.
	c.Set("Retry-After", "10")
	return c.Status(status).SendString(errorPage(requestLang, code, message))
}

// errorPage renders a small, dependency-free maintenance page localized to the
// request language. It deliberately never includes internal details (dial
// addresses, upstream URLs, stack traces).
func errorPage(lang, code, message string) string {
	dir := "ltr"
	if lang == "fa" {
		dir = "rtl"
	}

	title := i18n.TLang(lang, "gateway.title_unavailable")
	heading := i18n.TLang(lang, "gateway.heading_back_soon")
	body := i18n.TLang(lang, "gateway.body_back_soon")

	switch code {
	case "UPSTREAM_ERROR":
		heading = i18n.TLang(lang, "gateway.heading_upstream")
		body = i18n.TLang(lang, "gateway.body_upstream")
	case "SERVICE_UNAVAILABLE", "service_unavailable":
		heading = i18n.TLang(lang, "gateway.heading_unavailable")
		body = i18n.TLang(lang, "gateway.body_unavailable")
	case "HOST_NOT_FOUND", "HOST_MISMATCH", "ROUTE_NOT_FOUND":
		title = i18n.TLang(lang, "gateway.title_not_found")
		heading = i18n.TLang(lang, "gateway.heading_not_found")
		body = i18n.TLang(lang, "gateway.body_not_found")
	}

	tryAgain := i18n.TLang(lang, "common.try_again")
	reloadHint := i18n.TLang(lang, "common.reload_hint")

	// Escape message once for safe display (it may come from config).
	safeMsg := html.EscapeString(message)

	return `<!DOCTYPE html>
<html lang="` + lang + `" dir="` + dir + `">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + title + `</title>
<meta http-equiv="refresh" content="10">
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html, body { height: 100%; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Vazirmatn", "Tahoma", sans-serif;
    background: #f6f7f9; color: #1a1f2e;
    display: flex; align-items: center; justify-content: center; padding: 24px;
  }
  .card {
    max-width: 480px; width: 100%;
    background: #ffffff; border: 1px solid #e2e5ec; border-radius: 12px;
    padding: 40px 32px; text-align: center;
    box-shadow: 0 8px 30px rgba(20, 24, 40, 0.06);
  }
  .icon {
    width: 56px; height: 56px; margin: 0 auto 20px; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    background: #eef1f8;
  }
  .icon svg { width: 28px; height: 28px; }
  h1 { font-size: 20px; font-weight: 650; letter-spacing: -0.01em; margin-bottom: 8px; }
  p { font-size: 14.5px; line-height: 1.6; color: #5b6272; margin-bottom: 24px; }
  .detail { font-size: 12px; color: #9aa1b1; margin-top: -16px; margin-bottom: 24px; word-break: break-word; }
  button {
    font: inherit; font-size: 14px; font-weight: 600; color: #fff;
    background: #1a1f2e; border: 0; border-radius: 8px;
    padding: 10px 22px; cursor: pointer;
    transition: background 0.15s ease, transform 0.1s ease;
  }
  button:hover { background: #2b3147; }
  button:active { transform: translateY(1px); }
  .hint { font-size: 12px; color: #9aa1b1; margin-top: 16px; }
  @media (prefers-color-scheme: dark) {
    body { background: #0f1117; color: #e8eaf0; }
    .card { background: #171a22; border-color: #262b38; box-shadow: 0 8px 30px rgba(0,0,0,0.4); }
    .icon { background: #222838; }
    p { color: #a6adc0; }
    .detail, .hint { color: #6b7280; }
    button { background: #3b82f6; }
    button:hover { background: #2f6fe0; }
  }
</style>
</head>
<body>
<div class="card" role="alert">
  <div class="icon" aria-hidden="true">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
      <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
      <line x1="12" y1="9" x2="12" y2="13"/>
      <line x1="12" y1="17" x2="12.01" y2="17"/>
    </svg>
  </div>
  <h1>` + heading + `</h1>
  <p>` + body + `</p>
  <div class="detail">` + safeMsg + `</div>
  <button onclick="location.reload()">` + tryAgain + `</button>
  <div class="hint">` + reloadHint + `</div>
</div>
</body>
</html>`
}
