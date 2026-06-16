#!/usr/bin/env python3
# netqa SwiftBar plugin — shows live connectivity in the macOS menu bar.
#
# Install:
#   1. Install SwiftBar (https://swiftbar.app) and pick a plugin folder.
#   2. Copy this file there (keep the name "netqa.5s.py" — 5s = refresh interval).
#   3. chmod +x netqa.5s.py  and refresh SwiftBar.
#
# It reads the local netqa daemon (http://127.0.0.1:8799) — no extra config.

import json
import urllib.request
import urllib.error

PORT = 8799
BASE = f"http://127.0.0.1:{PORT}"


def get(path):
    with urllib.request.urlopen(BASE + path, timeout=2) as r:
        return json.load(r)


def fmt(x, d=1):
    try:
        return f"{float(x):.{d}f}"
    except Exception:
        return "—"


def main():
    try:
        st = get("/api/status")
    except Exception:
        # Daemon down / not running.
        print("netqa ⚠️")
        print("---")
        print("netqa daemon not reachable | color=#f85149")
        print(f"Open dashboard | href={BASE}")
        return

    online = st.get("online")
    outage = st.get("outage_open")
    oclass = st.get("outage_class", "")
    rtt = st.get("avg_rtt_ms") or 0
    jit = st.get("jitter_ms") or 0
    loss = st.get("loss_pct") or 0
    vpn = st.get("vpn")
    gwip = st.get("gateway_ip") or "—"
    gwup = st.get("gateway_up")
    iface = st.get("iface") or st.get("ssid") or "—"
    nid = st.get("network_id") or 0

    # --- menu bar title ---
    # When everything is fine, keep it to just the green dot — no numbers.
    # Surface detail only when there is a problem worth glancing at.
    if outage:
        title = f"🔴 {oclass.upper()}"
    elif not online:
        title = "🟡 local"
    elif loss >= 2:
        title = f"🟢 {fmt(loss,0)}%⬇"  # online but losing packets
    else:
        title = "🟢"
    print(title)

    # --- dropdown ---
    print("---")
    state = "Online" if online else ("OUTAGE (" + oclass + ")" if outage else "Offline (local)")
    color = "#3fb950" if online else ("#f85149" if outage else "#e3a008")
    print(f"{state} | color={color}")
    print("---")
    print(f"Latency  {fmt(rtt)} ms | font=Menlo")
    print(f"Jitter   {fmt(jit)} ms | font=Menlo")
    print(f"Loss     {fmt(loss)} % | font=Menlo")

    # latest throughput vs target (best effort)
    try:
        down = up = None
        if nid:
            hist = get(f"/api/history?network={nid}&hours=720&buckets=1")
            tps = hist.get("throughput") or []
            if tps:
                down, up = tps[-1].get("DownMbit"), tps[-1].get("UpMbit")
        target = None
        nets = {n["ID"]: n for n in get("/api/networks")}
        provs = {p["ID"]: p for p in get("/api/providers")}
        pid = (nets.get(nid) or {}).get("ProviderID")
        if pid in provs:
            target = provs[pid].get("TargetDownMbit")
        if down is not None:
            line = f"Down     {fmt(down)} Mbit"
            if target:
                line += f"  / {fmt(target,0)} target"
            print(line + " | font=Menlo")
            print(f"Up       {fmt(up)} Mbit | font=Menlo")
    except Exception:
        pass

    print("---")
    print(f"VPN        {'on' if vpn else 'off'} | font=Menlo")
    print(f"Link       {iface} | font=Menlo")
    print(f"Gateway    {'up' if gwup else 'down'} {gwip} | font=Menlo")

    print("---")
    print(f"Run speed test now | bash=/usr/bin/curl param1=-s param2=-X param3=POST "
          f"param4={BASE}/api/speedtest terminal=false refresh=true")
    print(f"Open dashboard | href={BASE}")
    print("Refresh | refresh=true")


if __name__ == "__main__":
    main()
