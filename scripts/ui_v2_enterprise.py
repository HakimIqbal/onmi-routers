#!/usr/bin/env python3
"""Kill Quota Tracker + full visual redesign (Enterprise Gateway palette)."""
from pathlib import Path
import re

p = Path("/home/ubuntu/onmi-routers/dashboard.html")
s = p.read_text()

# ── 1) Remove Quota Tracker nav button ──
s = re.sub(
    r'\s*<button class="nav-item" data-route="#/quota"[^>]*>[\s\S]*?Quota Tracker\s*</button>\s*',
    "\n",
    s,
    count=1,
)

# ── 2) Routes: drop quota, redirect legacy ──
s = re.sub(
    r"\s*'#/quota':\s*\{[^}]+\},?\n",
    "\n",
    s,
    count=1,
)

# router hash handling for quota
s = s.replace(
    "if (hash === '#/accounts' || hash === '#/providers' || hash === '#/quota') {\n"
    "    if (!window._acctTab) window._acctTab = 'cf';\n"
    "    if (typeof loadAccounts === 'function') loadAccounts();\n"
    "    if (hash === '#/quota' && typeof showTab === 'function') showTab(null, 'cf');\n"
    "  }",
    "if (hash === '#/accounts' || hash === '#/providers') {\n"
    "    if (!window._acctTab) window._acctTab = 'cf';\n"
    "    if (typeof loadAccounts === 'function') loadAccounts();\n"
    "  }",
)

# legacy #/quota → providers
if "hash === '#/quota'" not in s:
    s = s.replace(
        "var hash = window.location.hash || '#/endpoint';",
        "var hash = window.location.hash || '#/endpoint';\n"
        "  if (hash === '#/quota') hash = '#/providers';",
        1,
    )
else:
    # still may have other refs
    pass
s = s.replace(
    "var hash = window.location.hash || '#/endpoint';",
    "var hash = window.location.hash || '#/endpoint';\n"
    "  if (hash === '#/quota') { window.location.hash = '#/providers'; hash = '#/providers'; }",
    1,
)

# ── 3) Full :root token rewrite (Enterprise Gateway from ui-ux-pro-max) ──
root_re = re.compile(r":root\s*\{[\s\S]*?\n\}", re.M)
new_root = """:root {
  /* Enterprise Gateway — cool navy + emerald health accent (no AI-orange slop) */
  --bg:           #020617;
  --bg-panel:     #0B1220;
  --bg-elevated:  #111827;
  --bg-hover:     rgba(148,163,184,0.06);
  --bg-input:     #060B14;

  --text-primary:    #F1F5F9;
  --text-secondary:  #94A3B8;
  --text-tertiary:   #64748B;
  --text-quaternary: #475569;

  --brand:       #38BDF8;
  --brand-hover: #7DD3FC;
  --brand-violet:#818CF8;
  --brand-bg:    rgba(56,189,248,0.10);
  --brand-subtle:rgba(56,189,248,0.16);
  --accent-cyan: #38BDF8;

  --green:   #22C55E;
  --green-hover: #16A34A;
  --emerald: #34D399;
  --green-subtle: rgba(34,197,94,0.12);
  --red:     #F87171;
  --red-subtle:   rgba(248,113,113,0.12);
  --orange:  #FBBF24;
  --yellow:  #FACC15;
  --yellow-subtle:rgba(250,204,21,0.12);

  --border:        #1E293B;
  --border-strong: rgba(148,163,184,0.14);
  --border-bright: rgba(148,163,184,0.22);
  --border-muted:  #0F172A;

  --radius:    12px;
  --radius-sm: 8px;
  --radius-xs: 6px;
  --radius-lg: 16px;

  --shadow-card:   0 0 0 1px rgba(148,163,184,0.06), 0 8px 24px rgba(0,0,0,0.35);
  --shadow-hover:  0 0 0 1px rgba(56,189,248,0.18), 0 12px 32px rgba(0,0,0,0.4);
  --shadow-modal:  0 24px 64px rgba(0,0,0,0.55);

  --sp-1: 4px; --sp-2: 8px; --sp-3: 12px; --sp-4: 16px;
  --sp-5: 20px; --sp-6: 24px; --sp-8: 32px; --sp-10: 40px; --sp-12: 48px;

  --font: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
  --mono: 'JetBrains Mono', 'SF Mono', ui-monospace, Menlo, monospace;

  --tc: 160ms cubic-bezier(0.16, 1, 0.3, 1);
  --tt: 220ms cubic-bezier(0.16, 1, 0.3, 1);
}"""

m = root_re.search(s)
if not m:
    raise SystemExit(":root not found")
s = s[: m.start()] + new_root + s[m.end() :]
print("tokens ok")

# strip grid bg body override if messy — replace body opening
s = re.sub(
    r"body \{\n  background-color: var\(--bg\);\n  background-image:[\s\S]*?background-size: 24px 24px;\n",
    "body {\n",
    s,
    count=1,
)

# ── 4) Replace entire 9Router chrome CSS block ──
chrome_re = re.compile(r"/\* -- 9Router nav / chrome -- \*/[\s\S]*?(?=</style>)")
new_chrome = """/* -- Enterprise Gateway chrome -- */
body {
  background:
    radial-gradient(1200px 600px at 12% -10%, rgba(56,189,248,0.07), transparent 55%),
    radial-gradient(900px 500px at 100% 0%, rgba(34,197,94,0.05), transparent 50%),
    var(--bg) !important;
  color: var(--text-primary);
}
.sidebar {
  background: rgba(11,18,32,0.92) !important;
  backdrop-filter: blur(16px);
  -webkit-backdrop-filter: blur(16px);
  border-right: 1px solid var(--border) !important;
  width: 240px !important;
}
.sidebar-brand {
  padding: 18px 16px 14px !important;
  border-bottom: 1px solid var(--border);
  gap: 12px !important;
}
.sidebar-brand .logo {
  width: 34px; height: 34px;
  display: grid; place-items: center;
  background: linear-gradient(145deg, #0EA5E9, #0369A1) !important;
  color: #fff !important;
  border-radius: 10px !important;
  box-shadow: 0 0 0 1px rgba(56,189,248,0.25), 0 8px 20px rgba(14,165,233,0.18);
}
.sidebar-brand .title {
  font-size: 14.5px !important;
  font-weight: 650 !important;
  letter-spacing: -0.02em;
  color: #F8FAFC !important;
}
.version, .sidebar-brand .version {
  background: transparent !important;
  color: #64748B !important;
  border: 0 !important;
  font-size: 11px !important;
  font-family: var(--mono) !important;
  padding: 0 !important;
  opacity: 0.9;
}
.nav-section-label {
  color: #475569 !important;
  font-size: 10px !important;
  letter-spacing: 0.1em !important;
  margin: 18px 14px 8px !important;
  font-weight: 600 !important;
}
.nav-item {
  border-radius: 10px !important;
  margin: 2px 10px !important;
  padding: 9px 12px !important;
  color: #94A3B8 !important;
  gap: 10px !important;
  font-size: 13px !important;
  font-weight: 500 !important;
  border: 1px solid transparent !important;
  transition: background var(--tc), color var(--tc), border-color var(--tc) !important;
}
.nav-item svg { width: 16px; height: 16px; opacity: 0.9; flex-shrink: 0; }
.nav-item:hover {
  background: rgba(148,163,184,0.06) !important;
  color: #E2E8F0 !important;
}
.nav-item.active {
  background: rgba(56,189,248,0.10) !important;
  color: #7DD3FC !important;
  border-color: rgba(56,189,248,0.18) !important;
  box-shadow: none !important;
  font-weight: 600 !important;
}
.nav-item.active svg { color: #38BDF8 !important; stroke: #38BDF8 !important; opacity: 1; }
.sidebar-footer {
  border-top: 1px solid var(--border) !important;
  background: transparent !important;
}
.main { background: transparent !important; }
.topbar {
  background: rgba(2,6,23,0.65) !important;
  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  border-bottom: 1px solid var(--border) !important;
  padding: 14px 24px !important;
}
.topbar-title, #pageTitle {
  font-size: 18px !important;
  font-weight: 650 !important;
  letter-spacing: -0.03em !important;
  color: #F8FAFC !important;
}
.topbar-meta {
  color: #64748B !important;
  font-size: 12px !important;
  font-family: var(--mono) !important;
}
.pulse-dot {
  background: var(--green) !important;
  box-shadow: 0 0 0 3px rgba(34,197,94,0.18) !important;
}
.content { background: transparent !important; padding: 22px 24px 40px !important; max-width: 1400px; }
.panel, .card, .stat-card, .chart-box, .con-shell {
  background: linear-gradient(180deg, rgba(17,24,39,0.92), rgba(11,18,32,0.96)) !important;
  border: 1px solid var(--border) !important;
  border-radius: 14px !important;
  box-shadow: var(--shadow-card) !important;
}
.panel-head {
  border-bottom: 1px solid var(--border) !important;
  padding: 14px 16px !important;
}
.panel-head h3 {
  font-size: 13.5px !important;
  font-weight: 600 !important;
  letter-spacing: -0.01em;
  color: #F1F5F9 !important;
}
.stat-card {
  padding: 16px !important;
  transition: border-color var(--tc), transform var(--tc);
}
.stat-card:hover {
  border-color: rgba(56,189,248,0.22) !important;
  transform: translateY(-1px);
}
.stat-card .label {
  color: #64748B !important;
  font-size: 11px !important;
  letter-spacing: 0.06em !important;
  text-transform: uppercase !important;
  font-weight: 600 !important;
}
.stat-card .value {
  color: #F8FAFC !important;
  font-weight: 650 !important;
  letter-spacing: -0.03em !important;
  font-variant-numeric: tabular-nums;
}
.btn-primary, button.btn-primary {
  background: linear-gradient(180deg, #22C55E, #16A34A) !important;
  border: 1px solid rgba(34,197,94,0.45) !important;
  color: #052e16 !important;
  border-radius: 999px !important;
  font-weight: 650 !important;
  box-shadow: 0 1px 0 rgba(255,255,255,0.12) inset, 0 6px 16px rgba(34,197,94,0.18) !important;
}
.btn-primary:hover { filter: brightness(1.06); }
.btn-ghost {
  border-radius: 999px !important;
  border: 1px solid var(--border-strong) !important;
  background: rgba(15,23,42,0.6) !important;
  color: #CBD5E1 !important;
}
.btn-ghost:hover {
  border-color: rgba(56,189,248,0.35) !important;
  color: #E0F2FE !important;
  background: rgba(56,189,248,0.08) !important;
}
.subtab {
  border-radius: 8px !important;
  color: #94A3B8 !important;
}
.subtab.active, .range-tabs button.active {
  background: rgba(56,189,248,0.12) !important;
  color: #7DD3FC !important;
  border-color: transparent !important;
}
input, select, textarea, .mono-box {
  background: var(--bg-input) !important;
  border: 1px solid var(--border) !important;
  border-radius: 10px !important;
  color: var(--text-primary) !important;
  transition: border-color var(--tc), box-shadow var(--tc);
}
input:focus, select:focus, textarea:focus {
  outline: none !important;
  border-color: rgba(56,189,248,0.45) !important;
  box-shadow: 0 0 0 3px rgba(56,189,248,0.12) !important;
}
.tbl th {
  color: #64748B !important;
  font-size: 11px !important;
  letter-spacing: 0.06em !important;
  text-transform: uppercase !important;
  font-weight: 600 !important;
  background: rgba(2,6,23,0.35) !important;
  border-bottom: 1px solid var(--border) !important;
}
.tbl td {
  border-bottom: 1px solid rgba(30,41,59,0.85) !important;
  color: #CBD5E1 !important;
}
.tbl tr:hover td { background: rgba(56,189,248,0.04) !important; }
.ok, .sdot-ok { color: #4ADE80 !important; }
.err, .sdot-err, .stcode-5xx { color: #F87171 !important; }
.stcode-2xx { color: #4ADE80 !important; }
.stcode-4xx { color: #FBBF24 !important; }
.hist-err-badge {
  background: rgba(248,113,113,0.12) !important;
  color: #FCA5A5 !important;
  border: 1px solid rgba(248,113,113,0.25) !important;
}
.circuit-badge.circuit-closed { background: rgba(34,197,94,0.12); color: #86EFAC; }
.circuit-badge.circuit-open { background: rgba(248,113,113,0.12); color: #FCA5A5; }
.endpoint-row {
  display: flex; align-items: center; gap: 12px;
  padding: 12px 0; border-bottom: 1px solid var(--border);
}
.endpoint-row:last-child { border-bottom: 0; }
.endpoint-row label { width: 90px; color: #94A3B8; font-size: 13px; font-weight: 500; }
.endpoint-row input, .endpoint-row .mono-box {
  flex: 1; font-family: var(--mono); font-size: 12.5px;
  background: var(--bg-input) !important; border: 1px solid var(--border) !important;
  border-radius: 10px; color: #F1F5F9; padding: 10px 12px;
}
.modal, .modal-card, #detailModal .modal-card {
  background: #0B1220 !important;
  border: 1px solid var(--border) !important;
  border-radius: 16px !important;
  box-shadow: var(--shadow-modal) !important;
}
.status-dot { background: var(--green); box-shadow: 0 0 0 3px rgba(34,197,94,0.2); }
.status-dot.err { background: var(--red) !important; box-shadow: 0 0 0 3px rgba(248,113,113,0.2) !important; }

"""

if chrome_re.search(s):
    s = chrome_re.sub(new_chrome, s, count=1)
    print("chrome replaced")
else:
    # inject before </style>
    s = s.replace("</style>", new_chrome + "\n</style>", 1)
    print("chrome injected")

# kill leftover F97316 / orange brand hardcodes in inline styles if any critical
s = s.replace("#F97316", "#38BDF8")
s = s.replace("#FB923C", "#7DD3FC")
s = s.replace("#EA580C", "#0284C7")
s = s.replace("rgba(249,115,22", "rgba(56,189,248")

# labels polish
s = s.replace("Endpoint and Key", "Endpoint & Key", 1)

# getCss fallback color
s = s.replace("|| '#F97316'", "|| '#38BDF8'")
s = s.replace("|| \"#F97316\"", "|| \"#38BDF8\"")

p.write_text(s)
print("written", len(s))
checks = {
    "Quota Tracker": s.count("Quota Tracker"),
    "#/quota route": len(re.findall(r"'#/quota'", s)),
    "Enterprise Gateway chrome": s.count("Enterprise Gateway chrome"),
    "38BDF8": s.count("38BDF8"),
    "22C55E": s.count("22C55E"),
    "F97316": s.count("F97316"),
    "Endpoint & Key": s.count("Endpoint & Key"),
}
for k, v in checks.items():
    print(k, v)
