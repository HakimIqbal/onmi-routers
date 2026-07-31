// login_page.go — HTML template + error-injection helper for /login.
// Kept in a separate file to keep handlers.go focused on route logic.
package handlers

import (
	"html"
	"strings"
)

// loginPageHTML returns the login page with OnmiRouters branding.
const loginPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>OnmiRouters — Login</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;590;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<style>
:root {
  --bg: #0d1117; --bg-panel: #161b22; --bg-elevated: #21262d;
  --text: #e6edf3; --text-tertiary: #6e7681; --text-quaternary: #484f58;
  --brand: #6366f1; --brand-hover: #818cf8;
  --border: #30363d; --border-bright: rgba(255,255,255,0.16);
  --red: #f85149; --red-subtle: rgba(248,81,73,0.12);
  --radius: 8px; --radius-lg: 12px;
  --font: 'Inter', -apple-system, sans-serif; --mono: 'JetBrains Mono', monospace;
  --shadow-modal: 0 8px 32px rgba(0,0,0,0.5);
}
* { margin:0; padding:0; box-sizing:border-box; }
body {
  font-family: var(--font); background: var(--bg); color: var(--text);
  min-height: 100vh; display: flex; align-items: center; justify-content: center;
  -webkit-font-smoothing: antialiased;
}
.login-card {
  background: var(--bg-panel); border: 1px solid var(--border);
  border-radius: var(--radius-lg); padding: 40px; width: 90%; max-width: 400px;
  box-shadow: var(--shadow-modal);
}
.login-logo {
  width: 48px; height: 48px; border-radius: 12px;
  background: #454065; display: flex; align-items: center; justify-content: center;
  margin: 0 auto 20px; color: #fff;
  box-shadow: 0 4px 14px rgba(40,36,66,0.55), inset 0 0 0 1px rgba(255,255,255,0.08);
}
.login-title { text-align: center; font-size: 20px; font-weight: 590; margin-bottom: 6px; }
.login-sub { text-align: center; font-size: 13px; color: var(--text-tertiary); margin-bottom: 28px; }
.login-error {
  background: var(--red-subtle); color: var(--red); border: 1px solid rgba(248,81,73,0.3);
  border-radius: var(--radius); padding: 10px 14px; font-size: 13px; margin-bottom: 16px;
  text-align: center;
}
.login-field { margin-bottom: 16px; }
.login-label { font-size: 12px; color: var(--text-tertiary); display: block; margin-bottom: 6px; font-weight: 500; }
.login-input {
  width: 100%; padding: 10px 14px; background: var(--bg); border: 1px solid var(--border);
  border-radius: var(--radius); color: var(--text); font-family: var(--mono); font-size: 13px;
  transition: border-color 150ms ease;
}
.login-input:focus { outline: none; border-color: var(--brand); box-shadow: 0 0 0 3px rgba(99,102,241,0.15); }
.login-btn {
  width: 100%; padding: 11px; background: var(--brand); color: #fff; border: none;
  border-radius: var(--radius); font-size: 14px; font-weight: 590; cursor: pointer;
  font-family: var(--font); transition: background 150ms ease, box-shadow 150ms ease, transform 200ms ease;
  box-shadow: 0 1px 3px rgba(99,102,241,0.3);
}
.login-btn:hover { background: var(--brand-hover); box-shadow: 0 4px 12px rgba(99,102,241,0.4); transform: translateY(-1px); }
.login-btn:active { transform: translateY(0); }
.login-footer { text-align: center; margin-top: 20px; font-size: 11px; color: var(--text-quaternary); font-family: var(--mono); }
</style>
</head>
<body>
<div class="login-card">
  <div class="login-logo">
    <svg width="26" height="26" viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg">
      <circle cx="12" cy="12" r="2.7"/>
      <circle cx="12" cy="5.2" r="1.7"/>
      <circle cx="12" cy="18.8" r="1.7"/>
      <circle cx="6.8" cy="7" r="1.7"/>
      <circle cx="17.2" cy="7" r="1.7"/>
      <circle cx="6.8" cy="17" r="1.7"/>
      <circle cx="17.2" cy="17" r="1.7"/>
    </svg>
  </div>
  <div class="login-title">OnmiRouters</div>
  <div class="login-sub">Gateway Control Panel</div>
  <form method="POST" action="/login">
    <div class="login-field">
      <label class="login-label" for="key">Gateway API Key</label>
      <input class="login-input" type="password" id="key" name="key" placeholder="gw-..." autofocus required>
    </div>
    <button class="login-btn" type="submit">Sign In</button>
  </form>
  <div class="login-footer">OnmiRouters v1.7.9</div>
</div>
<script>
  // PULL: persist the key to localStorage on submit so the dashboard can use a
  // Bearer token (mirrors 9Router). Cookie auth alone can be blocked by tunnels/proxies.
  (function(){
    var f = document.querySelector('form[action="/login"]');
    if (!f) return;
    f.addEventListener('submit', function(){
      try {
        var k = document.getElementById('key');
        if (k && k.value) window.localStorage.setItem('gwkey', k.value.trim());
      } catch(e){}
    });
  })();
</script>
</body>
</html>`

// loginPageHTMLWithError returns the login page with an error message.
// The message is HTML-escaped to prevent reflected XSS if any future code
// path passes user-controlled input to this function.
func loginPageHTMLWithError(msg string) string {
	return strings.Replace(loginPageHTML,
		`<div class="login-sub">Gateway Control Panel</div>`,
		`<div class="login-sub">Gateway Control Panel</div><div class="login-error">`+html.EscapeString(msg)+`</div>`, 1)
}
