"use strict";
// netqa dashboard — vanilla JS, no external deps (works offline / on a flaky line).

const $ = (id) => document.getElementById(id);
const fmt = (n, d = 1) => (n == null ? "—" : Number(n).toFixed(d));

let state = {
  providers: [],
  networks: [],
  selectedProvider: "all", // "all" or provider id (string)
  presentation: false,
  history: null,
  liveNetworkID: 0,
};

// ---- data loading ----
async function getJSON(url, opts) {
  const r = await fetch(url, opts);
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}

async function loadProviders() {
  state.providers = (await getJSON("/api/providers")) || [];
  const sel = $("provider");
  const cur = sel.value;
  sel.innerHTML = `<option value="all">All providers</option>` +
    state.providers.map((p) => `<option value="${p.ID}">${esc(p.Name)}</option>`).join("");
  if (cur) sel.value = cur;
}

async function loadNetworks() {
  state.networks = (await getJSON("/api/networks")) || [];
  renderNetworks();
}

// Networks belonging to the currently selected provider (or all).
function activeNetworkIDs() {
  if (state.selectedProvider === "all") return state.networks.map((n) => n.ID);
  const pid = Number(state.selectedProvider);
  return state.networks.filter((n) => n.ProviderID === pid).map((n) => n.ID);
}

// For charts we focus one network: the live one if it matches the filter,
// else the first network of the selected provider.
function focusNetworkID() {
  const ids = activeNetworkIDs();
  if (ids.includes(state.liveNetworkID)) return state.liveNetworkID;
  return ids[0] || 0;
}

async function loadHistory() {
  const nid = focusNetworkID();
  if (!nid) { state.history = null; drawAll(); return; }
  const hours = $("range").value;
  state.history = await getJSON(`/api/history?network=${nid}&hours=${hours}&buckets=160`);
  state.history.networkID = nid;
  drawAll();
}

// ---- live status (SSE) ----
function startStream() {
  const es = new EventSource("/api/stream");
  es.onmessage = (e) => applyStatus(JSON.parse(e.data));
  es.onerror = () => { /* browser auto-reconnects; fine on flaky links */ };
  // also pull once immediately
  getJSON("/api/status").then(applyStatus).catch(() => {});
}

function applyStatus(s) {
  // Ignore the zero-value status emitted before the first probe tick completes,
  // so the UI never flashes a false "Offline".
  if (!s || !s.ts || s.ts.startsWith("0001")) {
    $("livetext").textContent = "starting…";
    return;
  }
  state.liveNetworkID = s.network_id || 0;
  const online = s.online;
  const cls = online ? "up" : (s.outage_open ? "down" : "warn");
  $("livedot").className = "dot " + cls;
  $("livepill").className = "pill " + cls;
  $("livetext").textContent = online ? "online" : (s.outage_open ? "OUTAGE (" + s.outage_class + ")" : "offline (local)");
  $("livenet").textContent = (s.ssid ? "Wi-Fi: " + s.ssid + "  " : "") + (s.vpn ? "· VPN on" : "");

  $("m_online").textContent = online ? "Online" : "Offline";
  $("m_online").style.color = online ? "var(--good)" : "var(--bad)";
  $("m_loss").textContent = fmt(s.loss_pct) + "%";
  $("m_rtt").textContent = fmt(s.avg_rtt_ms) + " ms";
  $("m_jit").textContent = fmt(s.jitter_ms) + " ms";
  $("m_vpn").textContent = s.vpn ? "on" : "off";
  // SSID is often hidden from background processes on macOS 14.4+ (needs Location
  // permission). Fall back to showing the connected interface instead of blank.
  $("m_ssid").textContent = s.ssid || s.iface || "—";
  $("m_gw").textContent = (s.gateway_up ? "up " : "down ") + (s.gateway_ip || "");
  const o = $("m_outage");
  if (s.outage_open) {
    o.innerHTML = `<span class="tag ${s.outage_class}">${s.outage_class.toUpperCase()} OUTAGE</span>
      <span class="muted">since ${new Date(s.outage_since).toLocaleTimeString()}</span>`;
  } else o.innerHTML = `<span class="muted">no active ISP outage</span>`;
}

// ---- rendering ----
function esc(s) { return (s || "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c])); }

function selectedProviderTargets() {
  if (state.selectedProvider === "all") return null;
  return state.providers.find((p) => p.ID === Number(state.selectedProvider)) || null;
}

function renderNetworks() {
  const opts = (sel) => `<option value="">— unassigned —</option>` +
    state.providers.map((p) => `<option value="${p.ID}" ${p.ID === sel ? "selected" : ""}>${esc(p.Name)}</option>`).join("");
  $("netrows").innerHTML = state.networks.map((n) => `
    <tr data-ssid="${esc(n.SSID)}" data-mac="${esc(n.GatewayMAC)}">
      <td>${esc(n.SSID) || "<span class='muted'>(none)</span>"}</td>
      <td class="muted">${esc(n.GatewayMAC)}</td>
      <td class="muted">${esc(n.ISPASN) || "—"}</td>
      <td><input class="lbl" value="${esc(n.Label)}" style="width:120px"></td>
      <td><select class="prov">${opts(n.ProviderID || 0)}</select></td>
      <td class="right"><button class="saveNet">save</button></td>
    </tr>`).join("");
  document.querySelectorAll("#netrows .saveNet").forEach((b) =>
    b.addEventListener("click", (e) => saveNetwork(e.target.closest("tr"))));
}

async function saveNetwork(tr) {
  const pidVal = tr.querySelector(".prov").value;
  await getJSON("/api/networks", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      ssid: tr.dataset.ssid, gateway_mac: tr.dataset.mac,
      provider_id: pidVal ? Number(pidVal) : null,
      label: tr.querySelector(".lbl").value,
    }),
  });
  await loadNetworks();
  await loadHistory();
}

// ---- canvas charts (minimal line/area, no libs) ----
function chart(canvasId, points, getY, opts = {}) {
  const cv = $(canvasId);
  const dpr = window.devicePixelRatio || 1;
  const w = cv.clientWidth, h = cv.clientHeight;
  cv.width = w * dpr; cv.height = h * dpr;
  const ctx = cv.getContext("2d"); ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, w, h);
  const padL = 38, padB = 16, padT = 8, padR = 8;
  const plotW = w - padL - padR, plotH = h - padT - padB;
  if (!points || !points.length) { ctx.fillStyle = "#8b97a7"; ctx.fillText("no data", padL, h / 2); return; }

  let max = opts.max != null ? opts.max : 0;
  points.forEach((p) => { const v = getY(p); if (v != null && v > max) max = v; });
  if (max <= 0) max = 1;
  if (opts.target && opts.target > max) max = opts.target * 1.1;

  const n = points.length;
  const x = (i) => padL + (n === 1 ? plotW / 2 : (i / (n - 1)) * plotW);
  const y = (v) => padT + plotH - (v / max) * plotH;

  // axes / gridlines
  ctx.strokeStyle = "#222a35"; ctx.fillStyle = "#8b97a7"; ctx.font = "10px sans-serif";
  for (let g = 0; g <= 4; g++) {
    const gy = padT + (g / 4) * plotH, val = max * (1 - g / 4);
    ctx.beginPath(); ctx.moveTo(padL, gy); ctx.lineTo(w - padR, gy); ctx.stroke();
    ctx.fillText(val.toFixed(0), 4, gy + 3);
  }

  // outage shading
  if (opts.outages && state.history) {
    const t0 = +new Date(state.history.from), t1 = +new Date(state.history.to);
    ctx.fillStyle = "rgba(248,81,73,0.18)";
    opts.outages.forEach((o) => {
      const s = Math.max(+new Date(o.Start), t0);
      const endD = outageEnd(o);
      const e = endD ? Math.min(+endD, t1) : t1; // ongoing -> extend to now/range end
      const xs = padL + ((s - t0) / (t1 - t0)) * plotW;
      const xe = padL + ((e - t0) / (t1 - t0)) * plotW;
      ctx.fillRect(xs, padT, Math.max(1, xe - xs), plotH);
    });
  }

  // target line
  if (opts.target) {
    ctx.strokeStyle = "#d29922"; ctx.setLineDash([5, 4]); ctx.beginPath();
    ctx.moveTo(padL, y(opts.target)); ctx.lineTo(w - padR, y(opts.target)); ctx.stroke();
    ctx.setLineDash([]); ctx.fillStyle = "#d29922"; ctx.fillText("target " + opts.target, padL + 4, y(opts.target) - 3);
  }

  // series line
  ctx.strokeStyle = opts.color || "#4493f8"; ctx.lineWidth = 1.5; ctx.beginPath();
  let started = false;
  points.forEach((p, i) => {
    const v = getY(p);
    if (v == null || (p.samples === 0 && opts.gapOnEmpty)) { started = false; return; }
    if (!started) { ctx.moveTo(x(i), y(v)); started = true; } else ctx.lineTo(x(i), y(v));
  });
  ctx.stroke();

  // dot markers so even a single populated bucket is visible on a wide range
  if (opts.dots) {
    ctx.fillStyle = opts.color || "#4493f8";
    points.forEach((p, i) => {
      const v = getY(p);
      if (v == null || (p.samples === 0 && opts.gapOnEmpty)) return;
      ctx.beginPath(); ctx.arc(x(i), y(v), 2, 0, Math.PI * 2); ctx.fill();
    });
  }

  if (opts.bars) {
    ctx.fillStyle = opts.color || "#4493f8";
    points.forEach((p, i) => { const v = getY(p); if (v != null) ctx.fillRect(x(i) - 2, y(v), 4, padT + plotH - y(v)); });
  }
}

function drawAll() {
  const hp = state.history ? state.history.points : [];
  chart("c_rtt", hp, (p) => (p.samples ? p.avg_rtt_ms : null),
    { color: "#4493f8", gapOnEmpty: true, dots: true, outages: state.history ? state.history.outages : [] });
  chart("c_loss", hp, (p) => (p.samples ? p.loss_pct : null),
    { color: "#f85149", max: 100, gapOnEmpty: true, dots: true });
  const tgt = selectedProviderTargets();
  const tps = state.history ? state.history.throughput : [];
  chart("c_tp", tps, (p) => p.DownMbit, { color: "#3fb950", bars: true, dots: true, target: tgt ? tgt.TargetDownMbit : 0 });
  updateThroughputStat(tps, tgt);
  renderOutages();
}

// updateThroughputStat fills the hero throughput card with the latest measured
// download vs the active provider's target, colouring by how close it is.
function updateThroughputStat(tps, tgt) {
  const el = $("m_tp"), bar = $("m_tpbar");
  const latest = tps && tps.length ? tps[tps.length - 1] : null;
  if (!latest) { el.textContent = "—"; el.style.color = ""; bar.style.width = "0"; return; }
  const down = latest.DownMbit;
  const target = tgt ? tgt.TargetDownMbit : 0;
  el.textContent = fmt(down) + (target ? " / " + target : "") + " Mbit";
  if (target > 0) {
    const pct = Math.max(0, Math.min(100, (down / target) * 100));
    const col = pct >= 90 ? "var(--good)" : pct >= 60 ? "var(--warn)" : "var(--bad)";
    bar.style.width = pct + "%"; bar.style.background = col; el.style.color = col;
  } else {
    bar.style.width = "0"; el.style.color = "";
  }
}

// outageEnd returns the end Date, or null when the outage is still ongoing
// (the Go zero time marshals as year 0001, which must not be treated as a date).
function outageEnd(o) {
  if (!o.End) return null;
  const d = new Date(o.End);
  return d.getUTCFullYear() < 2000 ? null : d;
}

function renderOutages() {
  const outs = (state.history && state.history.outages) ? state.history.outages : [];
  $("outrows").innerHTML = outs.length ? outs.slice().reverse().map((o) => {
    const start = new Date(o.Start), end = outageEnd(o);
    const dur = end ? human((end - start) / 1000) : human((Date.now() - start) / 1000) + " (ongoing)";
    return `<tr><td>${start.toLocaleString()}</td><td>${end ? end.toLocaleString() : "—"}</td>
      <td>${dur}</td><td><span class="tag ${o.Class}">${o.Class}</span></td></tr>`;
  }).join("") : `<tr><td colspan="4" class="muted">No ISP outages in range. 🎉 (or your line behaved)</td></tr>`;
}

function human(sec) {
  if (sec < 60) return Math.round(sec) + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m " + Math.round(sec % 60) + "s";
  return Math.floor(sec / 3600) + "h " + Math.floor((sec % 3600) / 60) + "m";
}

// ---- export ----
function exportCSV() {
  if (!state.history) return;
  const rows = [["bucket_time", "avg_rtt_ms", "loss_pct", "samples"]];
  (state.history.points || []).forEach((p) => rows.push([p.t, p.avg_rtt_ms, p.loss_pct, p.samples]));
  rows.push([]); rows.push(["outage_start", "outage_end", "class"]);
  (state.history.outages || []).forEach((o) => {
    const end = outageEnd(o);
    rows.push([o.Start, end ? end.toISOString() : "ongoing", o.Class]);
  });
  const csv = rows.map((r) => r.join(",")).join("\n");
  const prov = selectedProviderTargets();
  const name = "netqa-" + (prov ? prov.Name.replace(/\W+/g, "_") : "all") + "-" + new Date().toISOString().slice(0, 10) + ".csv";
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
  a.download = name; a.click();
}

// presentation mode: hide the networks-management + provider-edit panels so a
// screen-share / export shows only the selected provider's evidence.
function applyPresentation() {
  const on = state.presentation;
  document.body.classList.toggle("pres", on);
  $("provpanel").style.display = on ? "none" : "";
  $("netpanel").style.display = on ? "none" : "";
}

// ---- provider editing ----
async function saveProvider() {
  const sel = state.selectedProvider;
  const p = {
    Name: $("p_name").value || "Provider",
    Notes: $("p_notes").value,
    TargetDownMbit: Number($("p_down").value) || 0,
    TargetUpMbit: Number($("p_up").value) || 0,
  };
  if (sel !== "all") p.ID = Number(sel);
  await getJSON("/api/providers", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p),
  });
  $("onboard").hidden = true;
  await loadProviders();
  await loadNetworks();
  drawAll();
}

function fillProviderForm() {
  const p = selectedProviderTargets();
  $("p_name").value = p ? p.Name : "";
  $("p_notes").value = p ? p.Notes : "";
  $("p_down").value = p ? p.TargetDownMbit : "";
  $("p_up").value = p ? p.TargetUpMbit : "";
}

// ---- persisted UI preferences ----
const prefs = {
  get(k, def) { try { const v = localStorage.getItem("netqa." + k); return v === null ? def : v; } catch { return def; } },
  set(k, v) { try { localStorage.setItem("netqa." + k, v); } catch {} },
};

// ---- provider settings panel collapse ----
function setProvPanelOpen(open) {
  $("provpanel").classList.toggle("open", open);
  $("provbody").hidden = !open;
  $("provchevron").textContent = "▸"; // CSS rotates it when .open
  prefs.set("provOpen", open ? "1" : "0");
}
$("provtoggle").addEventListener("click", () => setProvPanelOpen($("provbody").hidden));

// ---- wire up ----
$("provider").addEventListener("change", (e) => {
  state.selectedProvider = e.target.value;
  prefs.set("provider", e.target.value);
  fillProviderForm(); loadHistory();
});
$("range").addEventListener("change", (e) => { prefs.set("range", e.target.value); loadHistory(); });
$("presmode").addEventListener("change", (e) => {
  state.presentation = e.target.checked;
  prefs.set("presentation", e.target.checked ? "1" : "0");
  applyPresentation();
});
$("export").addEventListener("click", exportCSV);
$("p_save").addEventListener("click", saveProvider);

// Click the throughput card to force an immediate speed test.
$("tpcard").addEventListener("click", runSpeedtestNow);
async function runSpeedtestNow() {
  const el = $("m_tp");
  if (el.dataset.busy) return; // ignore double-clicks while running
  el.dataset.busy = "1";
  const prev = el.textContent, prevCol = el.style.color;
  el.textContent = "testing…"; el.style.color = "var(--accent)";
  try {
    const r = await fetch("/api/speedtest", { method: "POST" });
    if (!r.ok) throw new Error(await r.text());
    await loadHistory(); // refresh chart + hero stat with the new point
  } catch (e) {
    el.textContent = "failed"; el.style.color = "var(--bad)";
    setTimeout(() => { el.textContent = prev; el.style.color = prevCol; }, 2500);
  } finally {
    delete el.dataset.busy;
  }
}

async function init() {
  await loadProviders();
  await loadNetworks();

  // Restore saved UI preferences.
  $("range").value = prefs.get("range", "24");
  const savedProvider = prefs.get("provider", "all");
  if ([...$("provider").options].some((o) => o.value === savedProvider)) {
    $("provider").value = savedProvider;
  }
  state.selectedProvider = $("provider").value;
  state.presentation = prefs.get("presentation", "0") === "1";
  $("presmode").checked = state.presentation;
  applyPresentation();
  fillProviderForm();

  // First run: no providers yet -> open the panel and show the onboarding note.
  if (state.providers.length === 0) {
    $("onboard").hidden = false;
    setProvPanelOpen(true);
  } else {
    setProvPanelOpen(prefs.get("provOpen", "0") === "1");
  }

  startStream();
  await loadHistory();
  setInterval(loadHistory, 30000); // refresh charts periodically
}
init().catch((e) => { $("livetext").textContent = "init error: " + e.message; });
