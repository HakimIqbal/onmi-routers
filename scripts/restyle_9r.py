#!/usr/bin/env python3
"""Restyle OnmiRouters dashboard to 9Router-like IA + visual system (robust)."""
from pathlib import Path
import re

p = Path("/home/ubuntu/onmi-routers/dashboard.html")
s = p.read_text()

# --- tokens: replace known values ---
replacements = [
    ("#0A0F1A", "#09090b"),
    ("#0F172A", "#18181b"),
    ("#1E293B", "#27272a"),
    ("#0B1220", "#0c0c0e"),
    ("#F8FAFC", "#FAFAFA"),
    ("#94A3B8", "#A1A1AA"),
    ("#64748B", "#71717A"),
    ("#475569", "#52525B"),
    ("#22D3EE", "#F97316"),
    ("#67E8F9", "#FB923C"),
    ("#818CF8", "#A855F7"),
    ("rgba(34,211,238,0.10)", "rgba(249,115,22,0.12)"),
    ("rgba(34,211,238,0.15)", "rgba(249,115,22,0.18)"),
    ("#f85149", "#EF4444"),
    ("rgba(248,81,73,0.12)", "rgba(239,68,68,0.12)"),
    ("#f59e0b", "#F97316"),
    ("#162033", "#1c1c1f"),
]
for a, b in replacements:
    s = s.replace(a, b)
print("token swaps done")

if "background-size: 24px 24px" not in s:
    s = s.replace(
        "body {",
        """body {
  background-color: var(--bg);
  background-image:
    linear-gradient(rgba(255,255,255,0.015) 1px, transparent 1px),
    linear-gradient(90deg, rgba(255,255,255,0.015) 1px, transparent 1px);
  background-size: 24px 24px;
""",
        1,
    )
    print("grid bg ok")

nav_css = """
/* -- 9Router nav / chrome -- */
.sidebar {
  background: #121214 !important;
  border-right: 1px solid var(--border) !important;
  width: 248px !important;
}
.sidebar-brand .title { letter-spacing: -0.02em; }
.sidebar-brand .logo {
  background: linear-gradient(135deg, #F97316, #EA580C) !important;
  color: #fff !important;
  border-radius: 10px !important;
  box-shadow: 0 0 0 1px rgba(249,115,22,0.3), 0 8px 20px rgba(249,115,22,0.15);
}
.version, .sidebar-brand .version {
  background: transparent !important;
  color: var(--text-tertiary) !important;
  border: 0 !important;
  font-size: 11px !important;
  padding: 0 !important;
}
.nav-section-label {
  color: #52525B !important;
  font-size: 10px !important;
  letter-spacing: 0.08em !important;
  margin: 16px 12px 6px !important;
}
.nav-item {
  border-radius: 10px !important;
  margin: 2px 8px !important;
  padding: 9px 12px !important;
  color: #A1A1AA !important;
  gap: 10px !important;
}
.nav-item:hover {
  background: rgba(255,255,255,0.04) !important;
  color: #FAFAFA !important;
}
.nav-item.active {
  background: rgba(249,115,22,0.14) !important;
  color: #FB923C !important;
  box-shadow: inset 3px 0 0 #F97316;
  font-weight: 600 !important;
}
.nav-item.active svg { color: #F97316 !important; stroke: #F97316 !important; }
.panel, .card, .stat-card {
  background: #18181b !important;
  border: 1px solid #27272a !important;
  border-radius: 14px !important;
  box-shadow: 0 1px 0 rgba(255,255,255,0.03) !important;
}
.btn-primary, button.btn-primary {
  background: #F97316 !important;
  border-color: #F97316 !important;
  color: #fff !important;
  border-radius: 999px !important;
  font-weight: 600 !important;
}
.btn-primary:hover { background: #FB923C !important; }
.btn-ghost {
  border-radius: 999px !important;
  border: 1px solid #3f3f46 !important;
}
.subtab.active, .range-tabs button.active {
  background: rgba(249,115,22,0.18) !important;
  color: #FB923C !important;
}
.topbar {
  background: transparent !important;
  border-bottom: 1px solid #27272a !important;
}
.content { background: transparent !important; }
.endpoint-row {
  display: flex; align-items: center; gap: 12px;
  padding: 12px 0; border-bottom: 1px solid #27272a;
}
.endpoint-row:last-child { border-bottom: 0; }
.endpoint-row label { width: 90px; color: #A1A1AA; font-size: 13px; }
.endpoint-row input, .endpoint-row .mono-box {
  flex: 1; font-family: var(--mono); font-size: 12.5px;
  background: #0c0c0e; border: 1px solid #3f3f46; border-radius: 10px;
  color: #FAFAFA; padding: 10px 12px;
}
"""

if "/* -- 9Router nav / chrome -- */" not in s:
    s = s.replace("</style>", nav_css + "\n</style>", 1)
    print("nav css ok")

old_nav_start = s.find('<aside class="sidebar">')
old_nav_end = s.find("</aside>")
if old_nav_start < 0 or old_nav_end < 0:
    raise SystemExit("sidebar not found")

new_sidebar = """<aside class="sidebar">
  <div class="sidebar-brand">
    <div class="logo">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="6" cy="6" r="2"/><circle cx="18" cy="6" r="2"/><circle cx="6" cy="18" r="2"/><circle cx="18" cy="18" r="2"/>
        <circle cx="12" cy="12" r="2"/>
        <path d="M8 6h8M6 8v8M18 8v8M8 18h8"/>
      </svg>
    </div>
    <div style="display:flex;flex-direction:column;line-height:1.15">
      <span class="title">OnmiRouters</span>
      <span class="version" id="version">vdev</span>
    </div>
  </div>
  <nav class="sidebar-nav">
    <button class="nav-item" data-route="#/endpoint" onclick="navigateTo('#/endpoint')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M2 12h20"/><path d="M12 2a15 15 0 0 1 0 20"/></svg>
      Endpoint and Key
    </button>
    <button class="nav-item" data-route="#/providers" onclick="navigateTo('#/providers')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="7" rx="1"/><rect x="2" y="14" width="20" height="7" rx="1"/></svg>
      Providers
    </button>
    <button class="nav-item" data-route="#/combos" onclick="navigateTo('#/combos')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2 2 7l10 5 10-5-10-5z"/><path d="m2 17 10 5 10-5"/><path d="m2 12 10 5 10-5"/></svg>
      Combos
    </button>
    <button class="nav-item" data-route="#/usage" onclick="navigateTo('#/usage')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 20V10"/><path d="M12 20V4"/><path d="M6 20v-6"/></svg>
      Usage
    </button>
    <button class="nav-item" data-route="#/quota" onclick="navigateTo('#/quota')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-3-6.7"/><path d="M21 3v6h-6"/></svg>
      Quota Tracker
    </button>
    <button class="nav-item" data-route="#/tokensaver" onclick="navigateTo('#/tokensaver')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M19 5c-1.5 0-2.8 1.4-3 2-3.5-1.5-7.5 0-9 3 3 6 3 12 9 10 2-.5 3-2 4-4 .5-3.5-1-7-1-11z"/></svg>
      Token Saver
    </button>
    <button class="nav-item" data-route="#/clitools" onclick="navigateTo('#/clitools')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="m7 15 3-3-3-3"/><path d="M13 15h4"/></svg>
      CLI Tools
    </button>

    <div class="nav-section-label">SYSTEM</div>
    <button class="nav-item" data-route="#/proxies" onclick="navigateTo('#/proxies')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="7" rx="1"/><rect x="2" y="14" width="20" height="7" rx="1"/></svg>
      Proxy Pools
    </button>
    <button class="nav-item" data-route="#/console" onclick="navigateTo('#/console')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="m4 17 6-6-6-6"/><path d="M12 19h8"/></svg>
      Console Log
    </button>
    <button class="nav-item" data-route="#/tunnel" onclick="navigateTo('#/tunnel')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="4" width="20" height="14" rx="2"/><path d="M6 20h12"/></svg>
      Remote
    </button>
    <button class="nav-item" data-route="#/" onclick="navigateTo('#/')">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2"/></svg>
      Settings
    </button>
  </nav>
  <div class="sidebar-footer" style="padding:12px 14px;border-top:1px solid var(--border);font-size:11px;color:var(--text-tertiary)">
    <div style="display:flex;align-items:center;gap:6px;margin-bottom:4px">
      <span class="status-dot" id="sidebarDot"></span>
      <span id="sidebarStatus">-</span>
    </div>
    <div id="footerUpdate">-</div>
  </div>
</aside>"""

s = s[:old_nav_start] + new_sidebar + s[old_nav_end + len("</aside>") :]
print("sidebar ok")

new_routes = """var routes = {
  /* 9Router-style IA — pages reuse existing DOM */
  '#/':           { page: 'page-dashboard', title: 'Settings', meta: 'Overview · health · quick test' },
  '#/endpoint':   { page: 'page-keys',      title: 'Endpoint', meta: 'API endpoint configuration' },
  '#/keys':       { page: 'page-keys',      title: 'Endpoint', meta: 'API endpoint configuration' },
  '#/providers':  { page: 'page-accounts',  title: 'Providers', meta: 'Manage your AI provider connections' },
  '#/accounts':   { page: 'page-accounts',  title: 'Providers', meta: 'Manage your AI provider connections' },
  '#/cf':         { page: 'page-accounts',  title: 'Providers', meta: 'Cloudflare · Workers AI pool' },
  '#/combos':     { page: 'page-models',    title: 'Combos', meta: 'Model combos with fallback' },
  '#/models':     { page: 'page-models',    title: 'Combos', meta: 'Models · aliases · combos' },
  '#/usage':      { page: 'page-analytics', title: 'Usage and Analytics', meta: 'Monitor API usage, tokens, and request logs' },
  '#/analytics':  { page: 'page-analytics', title: 'Usage and Analytics', meta: 'Monitor API usage, tokens, and request logs' },
  '#/quota':      { page: 'page-accounts',  title: 'Quota Tracker', meta: 'Track and manage API quota limits' },
  '#/tokensaver': { page: 'page-tokensaver', title: 'Token Saver', meta: 'Compress prompts and outputs to save tokens' },
  '#/clitools':   { page: 'page-clitools',  title: 'CLI Tools', meta: 'Configure CLI tools' },
  '#/proxies':    { page: 'page-proxies',   title: 'Proxy Pools', meta: 'HTTP/SOCKS5 upstream proxy pool' },
  '#/console':    { page: 'page-console',   title: 'Console Log', meta: 'Real-time gateway logs (SSE)' },
  '#/tunnel':     { page: 'page-tunnel',    title: 'Remote', meta: 'Cloudflare Tunnel · remote access' }
};"""

m = re.search(r"var routes = \{[\s\S]*?\n\};", s)
if not m:
    raise SystemExit("routes block not found")
s = s[: m.start()] + new_routes + s[m.end() :]
print("routes ok")

s = s.replace(
    """  if (hash === '#/keys') {
    showCurrentKey();
    loadGatewayKeys();
  }
  if (hash === '#/models') {
    loadModels();
  }""",
    """  if (hash === '#/keys' || hash === '#/endpoint') {
    showCurrentKey();
    loadGatewayKeys();
    if (typeof loadEndpointPage === 'function') loadEndpointPage();
  }
  if (hash === '#/models' || hash === '#/combos') {
    loadModels();
    if (hash === '#/combos' && typeof switchMTab === 'function') {
      try { switchMTab('combos'); } catch(_){}
    }
  }""",
    1,
)

s = s.replace(
    """  if (hash === '#/accounts') {
    if (!window._acctTab) window._acctTab = 'cf';
    if (typeof loadAccounts === 'function') loadAccounts();
  }
  if (hash === '#/cf') {
    if (typeof loadAccounts === 'function') loadAccounts();
    if (typeof showTab === 'function') showTab(null, 'cf');
    if (typeof loadCF === 'function') loadCF();
  }
  if (hash === '#/tokensaver') {
    if (typeof loadTokenSaver === 'function') loadTokenSaver();
  }
  if (hash === '#/analytics') {
    if (typeof loadAnalytics === 'function') loadAnalytics();
  }""",
    """  if (hash === '#/accounts' || hash === '#/providers' || hash === '#/quota') {
    if (!window._acctTab) window._acctTab = 'cf';
    if (typeof loadAccounts === 'function') loadAccounts();
    if (hash === '#/quota' && typeof showTab === 'function') showTab(null, 'cf');
  }
  if (hash === '#/cf') {
    if (typeof loadAccounts === 'function') loadAccounts();
    if (typeof showTab === 'function') showTab(null, 'cf');
    if (typeof loadCF === 'function') loadCF();
  }
  if (hash === '#/tokensaver') {
    if (typeof loadTokenSaver === 'function') loadTokenSaver();
  }
  if (hash === '#/analytics' || hash === '#/usage') {
    if (typeof loadAnalytics === 'function') loadAnalytics();
  }""",
    1,
)
print("router actions ok")

if "function loadEndpointPage" not in s:
    endpoint_fn = """
/* Endpoint page polish (9Router style) */
function loadEndpointPage() {
  try {
    var host = window.location.origin || '';
    var el = document.getElementById('endpointLocalUrl');
    if (el) el.value = host + '/v1';
  } catch (e) {}
}
"""
    if "setInterval(refresh, 5000);" in s:
        s = s.replace("setInterval(refresh, 5000);", endpoint_fn + "\nsetInterval(refresh, 5000);", 1)
        print("loadEndpointPage injected")
    else:
        print("WARN no init inject point")

s = s.replace("<h3>Gateway API Keys</h3>", "<h3>API Keys</h3>", 1)
s = s.replace("<h3>Accounts &amp; Keys</h3>", "<h3>Providers</h3>", 1)
s = s.replace(
    "Gateway API keys are stored in Redis with per-key RPM limits, token quotas, and usage tracking.",
    "Requests without a valid key will be rejected when require-key is on. Keys live in Redis with per-key RPM and quotas.",
    1,
)

if 'id="endpointLocalUrl"' not in s:
    endpoint_card = """
      <div class="panel" style="margin-bottom:16px">
        <div class="panel-head">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="18" height="18"><path d="M12 2 2 7l10 5 10-5-10-5z"/></svg>
          <h3>API Endpoint</h3>
        </div>
        <div style="padding:8px 16px 16px">
          <div class="endpoint-row">
            <label>Local</label>
            <input id="endpointLocalUrl" class="mono-box" readonly value="" />
            <button class="btn-ghost" onclick="navigator.clipboard.writeText(document.getElementById('endpointLocalUrl').value)" style="padding:8px 12px">Copy</button>
          </div>
          <div class="endpoint-row">
            <label>Tunnel</label>
            <div class="mono-box" style="opacity:.7">Configure under Remote</div>
            <button class="btn-ghost" onclick="navigateTo('#/tunnel')" style="padding:8px 12px;background:rgba(168,85,247,.15);border-color:rgba(168,85,247,.4);color:#E9D5FF">Enable</button>
          </div>
        </div>
      </div>
"""
    s = s.replace('<div class="page" id="page-keys">', '<div class="page" id="page-keys">' + endpoint_card, 1)
    print("endpoint card ok")

s = s.replace(
    "var hash = window.location.hash || '#/';",
    "var hash = window.location.hash || '#/endpoint';",
    1,
)

p.write_text(s)
print("written", len(s))
for needle in [
    "#/endpoint",
    "#/providers",
    "#/usage",
    "#/quota",
    "#/combos",
    "F97316",
    "loadEndpointPage",
    "endpointLocalUrl",
    "Endpoint and Key",
    "9Router nav",
]:
    print(needle, s.count(needle))
