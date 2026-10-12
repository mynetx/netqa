"use strict";
// netqa dashboard — vanilla JS, no external deps (works offline / on a flaky line).

const $ = (id) => document.getElementById(id);
const fmt = (n, d = 1) => (n == null ? "—" : Number(n).toFixed(d));

// ---- time display ----
// Evidence is about a line in one place, so times can be pinned to that place's
// zone (e.g. the ISP's) regardless of where the viewer's laptop currently is.
// "" = the browser's own zone.
let displayTZ = "";
const tzOpt = () => (displayTZ ? { timeZone: displayTZ } : {});
const fmtDateTime = (d) => new Date(d).toLocaleString(undefined, { ...tzOpt(), hour12: false });
const fmtTime = (d) => new Date(d).toLocaleTimeString(undefined, { ...tzOpt(), hour12: false });
function tzLabel() {
  const name = displayTZ || Intl.DateTimeFormat().resolvedOptions().timeZone;
  const off = new Intl.DateTimeFormat("en", { timeZone: name, timeZoneName: "shortOffset" })
    .formatToParts(new Date()).find((p) => p.type === "timeZoneName");
  return name + (off ? " (" + off.value + ")" : "");
}

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
  // The live network may have become known while this request was in flight.
  if (focusNetworkID() !== nid) return loadHistory();
  drawAll();
}

// ---- live status (SSE) ----
// The browser reconnects EventSource on its own; what it does not do is tell the
// user the numbers on screen are frozen. lastLive drives a "stale" pill instead.
let lastLive = 0;
function startStream() {
  const es = new EventSource("/api/stream");
  es.onmessage = (e) => { lastLive = Date.now(); applyStatus(JSON.parse(e.data)); };
  setInterval(() => {
    if (lastLive && Date.now() - lastLive > 5000) {
      $("livedot").className = "dot warn";
      $("livepill").className = "pill warn";
      $("livetext").textContent = "dashboard disconnected — reconnecting…";
    }
  }, 2000);
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
  const prevLive = state.liveNetworkID;
  state.liveNetworkID = s.network_id || 0;
  // The live network can arrive after (or change since) the last chart load;
  // refocus the charts on it right away instead of waiting for the next poll.
  if (state.liveNetworkID !== prevLive && state.history && focusNetworkID() !== state.history.networkID) {
    refreshHistory();
  }
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
  $("m_metered").hidden = !s.metered;
  // SSID is often hidden from background processes on macOS 14.4+ (needs Location
  // permission). Fall back to showing the connected interface instead of blank.
  $("m_ssid").textContent = s.ssid || s.iface || "—";
  $("m_gw").textContent = (s.gateway_up ? "up " : "down ") + (s.gateway_ip || "");
  const o = $("m_outage");
  if (s.outage_open) {
    const oc = outageClass(s.outage_class);
    o.innerHTML = `<span class="tag ${oc}">${oc.toUpperCase()} OUTAGE</span>
      <span class="muted">since ${esc(fmtTime(s.outage_since))}</span>`;
  } else o.innerHTML = `<span class="muted">no active ISP outage</span>`;
}

// ---- rendering ----
// esc makes any value safe for both HTML text and quoted attribute contexts.
const ESC = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
function esc(s) { return s == null ? "" : String(s).replace(/[&<>"']/g, (c) => ESC[c]); }

// outageClass whitelists the server's class string before it becomes a CSS class.
const outageClass = (c) => (["isp", "upstream", "local"].includes(c) ? c : "local");

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
      <td><input class="lbl" value="${esc(n.Label)}" aria-label="Label for ${esc(n.SSID || n.GatewayMAC)}" style="width:120px"></td>
      <td><select class="prov" aria-label="Provider for ${esc(n.SSID || n.GatewayMAC)}">${opts(n.ProviderID || 0)}</select></td>
      <td class="right"><button class="saveNet">save</button></td>
    </tr>`).join("");
  document.querySelectorAll("#netrows .saveNet").forEach((b) =>
    b.addEventListener("click", (e) => saveNetwork(e.target.closest("tr"))));
}

// toast shows a short, screen-reader-announced message (errors stay longer).
let toastTimer;
function toast(msg, bad = false) {
  const t = $("toast");
  t.textContent = msg;
  t.className = bad ? "bad" : "";
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, bad ? 6000 : 2500);
}

// guarded runs an async UI action and reports failure instead of dropping it.
const guarded = (what, fn) => (...a) => fn(...a).catch((e) => toast(what + " failed: " + e.message, true));

const saveNetwork = guarded("Saving network", async (tr) => {
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
  toast("Network saved");
});

// bucketThroughput groups raw speedtest rows into `n` equal time buckets over
// [from,to], averaging DownMbit per bucket. Empty buckets get DownMbit=null so
// the chart skips them — this puts each bucket at its true time position (index
// == time), so the throughput chart scales with the range just like latency/loss.
function bucketThroughput(tps, from, to, n) {
  const t0 = +new Date(from), t1 = +new Date(to);
  const span = t1 - t0;
  if (n < 1 || span <= 0) return [];
  const width = span / n;
  const sum = new Array(n).fill(0), cnt = new Array(n).fill(0);
  (tps || []).forEach((p) => {
    const idx = Math.floor((+new Date(p.TS) - t0) / width);
    if (idx < 0 || idx >= n) return;
    sum[idx] += p.DownMbit; cnt[idx]++;
  });
  const out = new Array(n);
  for (let i = 0; i < n; i++) {
    out[i] = { TS: new Date(t0 + i * width).toISOString(), DownMbit: cnt[i] ? sum[i] / cnt[i] : null };
  }
  return out;
}

// ---- canvas charts (minimal line/area, no libs) ----
const charts = {}; // canvasId -> geometry + redraw closure, populated by chart()

// css reads a theme colour so canvas charts follow light/dark mode.
const css = (v) => getComputedStyle(document.documentElement).getPropertyValue(v).trim();

function chart(canvasId, points, getY, opts = {}) {
  const cv = $(canvasId);
  const dpr = window.devicePixelRatio || 1;
  const w = cv.clientWidth, h = cv.clientHeight;
  cv.width = w * dpr; cv.height = h * dpr;
  const ctx = cv.getContext("2d"); ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, w, h);
  const padL = 38, padB = 16, padT = 8, padR = 8;
  const plotW = w - padL - padR, plotH = h - padT - padB;
  if (!points || !points.length) { ctx.fillStyle = css("--mut"); ctx.font = "12px sans-serif"; ctx.fillText("no data", padL, h / 2); charts[canvasId] = null; return; }

  let max = opts.max != null ? opts.max : 0;
  points.forEach((p) => { const v = getY(p); if (v != null && v > max) max = v; });
  if (max <= 0) max = 1;
  if (opts.target && opts.target > max) max = opts.target * 1.1;

  const n = points.length;
  const x = (i) => padL + (n === 1 ? plotW / 2 : (i / (n - 1)) * plotW);
  const y = (v) => padT + plotH - (v / max) * plotH;

  // axes / gridlines
  ctx.strokeStyle = css("--line"); ctx.fillStyle = css("--mut"); ctx.font = "10px sans-serif";
  const dp = max < 10 ? 1 : 0; // keep small values (e.g. 0.5% loss) readable
  for (let g = 0; g <= 4; g++) {
    const gy = padT + (g / 4) * plotH, val = max * (1 - g / 4);
    ctx.beginPath(); ctx.moveTo(padL, gy); ctx.lineTo(w - padR, gy); ctx.stroke();
    ctx.fillText(val.toFixed(dp), 4, gy + 3);
  }

  // outage shading
  const bands = [];
  if (opts.outages && state.history) {
    const t0 = +new Date(state.history.from), t1 = +new Date(state.history.to);
    ctx.fillStyle = "rgba(248,81,73,0.18)";
    opts.outages.forEach((o) => {
      const s = Math.max(+new Date(o.Start), t0);
      const endD = outageEnd(o);
      const e = endD ? Math.min(+endD, t1) : t1; // ongoing -> extend to now/range end
      const xs = padL + ((s - t0) / (t1 - t0)) * plotW;
      const xe = padL + ((e - t0) / (t1 - t0)) * plotW;
      const bw = Math.max(1, xe - xs);
      ctx.fillRect(xs, padT, bw, plotH);
      bands.push({ o, xs, xe: xs + bw });
    });
  }

  // target line
  if (opts.target) {
    ctx.strokeStyle = css("--warn"); ctx.setLineDash([5, 4]); ctx.beginPath();
    ctx.moveTo(padL, y(opts.target)); ctx.lineTo(w - padR, y(opts.target)); ctx.stroke();
    ctx.setLineDash([]); ctx.fillStyle = css("--warn"); ctx.fillText("target " + opts.target, padL + 4, y(opts.target) - 3);
  }

  // series line
  ctx.strokeStyle = opts.color; ctx.lineWidth = 1.5; ctx.beginPath();
  let started = false;
  points.forEach((p, i) => {
    const v = getY(p);
    if (v == null || (p.samples === 0 && opts.gapOnEmpty)) { started = false; return; }
    if (!started) { ctx.moveTo(x(i), y(v)); started = true; } else ctx.lineTo(x(i), y(v));
  });
  ctx.stroke();

  // dot markers so even a single populated bucket is visible on a wide range
  if (opts.dots) {
    ctx.fillStyle = opts.color;
    points.forEach((p, i) => {
      const v = getY(p);
      if (v == null || (p.samples === 0 && opts.gapOnEmpty)) return;
      ctx.beginPath(); ctx.arc(x(i), y(v), 2, 0, Math.PI * 2); ctx.fill();
    });
  }

  if (opts.bars) {
    ctx.fillStyle = opts.color;
    points.forEach((p, i) => { const v = getY(p); if (v != null) ctx.fillRect(x(i) - 2, y(v), 4, padT + plotH - y(v)); });
  }

  // stash geometry + a redraw closure so hover can map cursor->data and repaint
  charts[canvasId] = {
    ctx, points, getY, x, y, n, color: opts.color,
    padL, padT, plotW, plotH, bands,
    redraw: () => chart(canvasId, points, getY, opts),
  };
}

function drawAll() {
  const hp = state.history ? state.history.points : [];
  chart("c_rtt", hp, (p) => (p.samples ? p.avg_rtt_ms : null),
    { color: css("--accent"), gapOnEmpty: true, dots: true, outages: state.history ? state.history.outages : [] });
  // max is a floor, not a cap: low loss stays visible instead of hugging 0 on a 0–100 axis.
  chart("c_loss", hp, (p) => (p.samples ? p.loss_pct : null),
    { color: css("--bad"), max: 5, gapOnEmpty: true, dots: true });
  const tgt = selectedProviderTargets();
  const tps = state.history ? state.history.throughput : [];
  const tpb = state.history ? bucketThroughput(tps, state.history.from, state.history.to, 160) : [];
  chart("c_tp", tpb, (p) => p.DownMbit, { color: css("--good"), bars: true, dots: true, target: tgt ? tgt.TargetDownMbit : 0 });
  updateThroughputStat(tps, tgt);
  renderOutages();
  $("outtz").textContent = tzLabel();
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
  const trs = (state.history && state.history.traceroutes) ? state.history.traceroutes : [];
  const byOutage = {};
  trs.forEach((t) => { if (t.OutageID) (byOutage[t.OutageID] = byOutage[t.OutageID] || []).push(t); });

  $("outrows").innerHTML = outs.length ? outs.slice().reverse().map((o) => {
    const start = new Date(o.Start), end = outageEnd(o);
    const dur = end ? human((end - start) / 1000) : human((Date.now() - start) / 1000) + " (ongoing)";
    const paths = byOutage[o.ID] || [];
    const pathCell = paths.length
      ? `<button class="pathtoggle" data-outage="${o.ID}" aria-expanded="false" aria-controls="path_${o.ID}">path ▾</button>`
      : `<span class="muted">—</span>`;
    const detail = paths.length
      ? `<tr class="pathrow" id="path_${o.ID}" hidden><td colspan="5"><pre class="trace">` +
        paths.map((t) => `→ ${esc(t.Target)} @ ${esc(fmtTime(t.TS))}\n${esc(t.Raw)}`).join("\n\n") +
        `</pre></td></tr>`
      : "";
    const oc = outageClass(o.Class);
    return `<tr><td>${esc(fmtDateTime(start))}</td><td>${end ? esc(fmtDateTime(end)) : "—"}</td>` +
      `<td>${dur}</td><td><span class="tag ${oc}">${oc}</span></td><td>${pathCell}</td></tr>${detail}`;
  }).join("") : `<tr><td colspan="5" class="muted">No ISP outages in range. 🎉 (or your line behaved)</td></tr>`;

  document.querySelectorAll(".pathtoggle").forEach((b) => {
    b.onclick = () => {
      const row = $("path_" + b.dataset.outage);
      if (!row) return;
      row.hidden = !row.hidden;
      b.setAttribute("aria-expanded", String(!row.hidden));
    };
  });
}

function human(sec) {
  if (sec < 60) return Math.round(sec) + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m " + Math.round(sec % 60) + "s";
  return Math.floor(sec / 3600) + "h " + Math.floor((sec % 3600) / 60) + "m";
}

// ---- chart hover: crosshair + dot + floating tooltip ----
// fmtTs renders a point time as "Mon 18 June 21:00" (24h, display timezone).
function fmtTs(t) {
  const parts = new Intl.DateTimeFormat(undefined, {
    ...tzOpt(), weekday: "short", day: "numeric", month: "long",
    hour: "2-digit", minute: "2-digit", hour12: false,
  }).formatToParts(new Date(t));
  const g = (type) => (parts.find((p) => p.type === type) || {}).value || "";
  return `${g("weekday")} ${g("day")} ${g("month")} ${g("hour")}:${g("minute")}`;
}

function showTip(html, clientX, clientY) {
  const tip = $("tip");
  tip.innerHTML = html;
  tip.hidden = false;
  const m = 14, r = tip.getBoundingClientRect();
  let left = clientX + m, top = clientY + m;
  if (left + r.width > window.innerWidth) left = clientX - m - r.width;
  if (top + r.height > window.innerHeight) top = clientY - m - r.height;
  tip.style.left = Math.max(4, left) + "px";
  tip.style.top = Math.max(4, top) + "px";
}
function hideTip() { $("tip").hidden = true; }

// attachHover wires mousemove/leave on a canvas. kind drives the value readout:
// "rtt" (ms, with outage hit-test), "loss" (%), or "tp" (Mbit).
function attachHover(canvasId, kind) {
  const cv = $(canvasId);
  if (!cv || cv.dataset.hover) return; // bind once
  cv.dataset.hover = "1";

  cv.addEventListener("mousemove", (ev) => {
    const c = charts[canvasId];
    if (!c || !c.n) { hideTip(); return; }
    const rect = cv.getBoundingClientRect();
    const mx = ev.clientX - rect.left;
    if (mx < c.padL || mx > c.padL + c.plotW) { hideTip(); c.redraw(); return; }

    // latency: outage band takes priority over the point readout
    if (kind === "rtt") {
      const band = (c.bands || []).find((b) => mx >= b.xs && mx <= b.xe);
      if (band) {
        const o = band.o, start = new Date(o.Start), endD = outageEnd(o);
        const len = endD ? human((endD - start) / 1000)
                         : human((Date.now() - start) / 1000) + " (ongoing)";
        c.redraw();
        c.ctx.fillStyle = "rgba(248,81,73,0.32)";
        c.ctx.fillRect(band.xs, c.padT, Math.max(1, band.xe - band.xs), c.plotH);
        showTip(
          `<b class="tag ${outageClass(o.Class)}">${outageClass(o.Class)}</b> outage<br>` +
          `${fmtTs(start)} → ${endD ? fmtTs(endD) : "now"}<br>` +
          `<b>${len}</b>`,
          ev.clientX, ev.clientY);
        return;
      }
    }

    const i = Math.round(((mx - c.padL) / c.plotW) * (c.n - 1));
    const p = c.points[i];
    if (!p) { hideTip(); c.redraw(); return; }
    const v = c.getY(p);
    const px = c.x(i);

    c.redraw();
    c.ctx.strokeStyle = css("--mut"); c.ctx.lineWidth = 1;
    c.ctx.beginPath(); c.ctx.moveTo(px, c.padT); c.ctx.lineTo(px, c.padT + c.plotH); c.ctx.stroke();
    if (v != null) {
      c.ctx.fillStyle = c.color;
      c.ctx.beginPath(); c.ctx.arc(px, c.y(v), 3.5, 0, Math.PI * 2); c.ctx.fill();
    }

    const t = kind === "tp" ? p.TS : p.t;
    let val;
    if (v == null) val = "no data";
    else if (kind === "rtt") val = fmt(v) + " ms";
    else if (kind === "loss") val = fmt(v) + " % loss";
    else val = fmt(v) + " Mbit";
    showTip(`${fmtTs(t)}<br><b>${val}</b>`, ev.clientX, ev.clientY);
  });

  cv.addEventListener("mouseleave", () => {
    hideTip();
    const c = charts[canvasId];
    if (c) c.redraw();
  });
}

// ---- export ----
function exportCSV() {
  if (!state.history) return;
  const rows = [["bucket_time_utc", "avg_rtt_ms", "loss_pct", "samples"]];
  (state.history.points || []).forEach((p) => rows.push([p.t, p.avg_rtt_ms, p.loss_pct, p.samples]));
  rows.push([]); rows.push(["outage_start_utc", "outage_end_utc", "class"]);
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
const saveProvider = guarded("Saving provider", async () => {
  const sel = state.selectedProvider;
  const p = {
    Name: $("p_name").value || "Provider",
    Notes: $("p_notes").value,
    TargetDownMbit: Number($("p_down").value) || 0,
    TargetUpMbit: Number($("p_up").value) || 0,
    MatchMACs: $("p_macs").value,
    MatchASN: $("p_asn").value,
  };
  if (sel !== "all") p.ID = Number(sel);
  await getJSON("/api/providers", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p),
  });
  $("onboard").hidden = true;
  await loadProviders();
  await loadNetworks();
  drawAll();
  toast("Provider saved");
});

function fillProviderForm() {
  const p = selectedProviderTargets();
  $("p_name").value = p ? p.Name : "";
  $("p_notes").value = p ? p.Notes : "";
  $("p_down").value = p ? p.TargetDownMbit : "";
  $("p_up").value = p ? p.TargetUpMbit : "";
  $("p_macs").value = p ? p.MatchMACs || "" : "";
  $("p_asn").value = p ? p.MatchASN || "" : "";
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
  $("provtoggle").setAttribute("aria-expanded", String(open));
  prefs.set("provOpen", open ? "1" : "0");
}
$("provtoggle").addEventListener("click", () => setProvPanelOpen($("provbody").hidden));

// ---- wire up ----
$("provider").addEventListener("change", (e) => {
  state.selectedProvider = e.target.value;
  prefs.set("provider", e.target.value);
  fillProviderForm(); refreshHistory();
});
$("range").addEventListener("change", (e) => { prefs.set("range", e.target.value); refreshHistory(); });
$("tz").addEventListener("change", (e) => {
  displayTZ = e.target.value;
  prefs.set("tz", displayTZ);
  drawAll();
});
$("presmode").addEventListener("change", (e) => {
  state.presentation = e.target.checked;
  prefs.set("presentation", e.target.checked ? "1" : "0");
  applyPresentation();
});
$("export").addEventListener("click", exportCSV);
$("p_save").addEventListener("click", saveProvider);

// Click the throughput card to force an immediate speed test.
$("tpcard").addEventListener("click", runSpeedtestNow);
$("tpcard").addEventListener("keydown", (e) => {
  if (e.key === "Enter" || e.key === " ") { e.preventDefault(); runSpeedtestNow(); }
});
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
    toast("Speed test failed: " + e.message, true);
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
  displayTZ = prefs.get("tz", "");
  if ([...$("tz").options].some((o) => o.value === displayTZ)) $("tz").value = displayTZ;
  else displayTZ = "";
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

  attachHover("c_rtt", "rtt");
  attachHover("c_loss", "loss");
  attachHover("c_tp", "tp");

  startStream();
  await loadHistory();
  scheduleHistory();
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshHistory(); });
  let rz;
  window.addEventListener("resize", () => { clearTimeout(rz); rz = setTimeout(drawAll, 150); });
}

// Charts refresh on a timer that scales with the range (a 30-day view gains
// nothing from a 30s poll) and skips work while the tab is hidden.
let historyTimer;
function scheduleHistory() {
  clearTimeout(historyTimer);
  const hours = Number($("range").value);
  historyTimer = setTimeout(refreshHistory, hours <= 24 ? 30000 : 120000);
}
function refreshHistory() {
  if (document.hidden) return scheduleHistory();
  loadHistory().catch((e) => toast("Refreshing charts failed: " + e.message, true)).finally(scheduleHistory);
}
init().catch((e) => { $("livetext").textContent = "init error: " + e.message; });
