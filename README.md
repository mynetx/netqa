# netqa

Connectivity-quality logger + local dashboard for macOS. Built to gather
**defensible evidence** that a flaky ISP line (e.g. a shared fiber that drops
pings and never reaches its advertised Mbit) needs to be fixed — without
false-accusing the ISP for self-inflicted gaps (Wi-Fi off, sleep, VPN).

## What it does

- **Continuous probing** every few seconds to multiple targets (ICMP without
  root, TCP:443 fallback). Computes rolling loss %, average RTT, jitter.
- **Idle-aware throughput** tests vs an **editable per-provider target** (40 Mbit
  now, 20 next month — just change it). Skips the test when the link is busy so
  it never fights your own traffic on a constrained line.
- **Outage classifier** with a defensible definition:
  `outage = awake AND link up AND gateway reachable AND internet unreachable`.
  Gaps from sleep, Wi-Fi off, or a down local router are logged as **local** and
  never counted against the ISP. Only **isp**/**upstream** gaps are evidence.
- **Multi-provider**, VPN-proof identity. Networks are keyed by their local
  fingerprint (SSID + **gateway MAC**), read from the *physical* default route —
  so a VPN to Germany never changes which provider the data belongs to. ASN
  enrichment is fetched **only when no VPN is active**.
- **Dashboard** at `http://127.0.0.1:8799`: live status (SSE), latency/loss/
  throughput charts with the target line, outage timeline, **per-provider
  filter**, **presentation mode** (lock the view + export to one provider so a
  screen-share/PDF for one provider never leaks another's data), and CSV export.
- **Alerts**: macOS notification on confirmed outage + recovery only.

## Requirements

- macOS (uses `route`, `arp`, `netstat`, `networksetup`, `pmset`, `osascript`)
- [Go](https://go.dev/dl/) 1.26+ to build

## Install (one command)

```sh
git clone https://github.com/mynetx/netqa.git
cd netqa
./install.sh
```

`install.sh` builds the binary to `~/.local/bin/netqa`, installs a launchd agent
that starts netqa at login (and restarts it if it dies), and prints the dashboard
URL. Re-run any time to update. Remove with `./uninstall.sh`.

Then open the dashboard: **http://127.0.0.1:8799**

## Manual build / run

```sh
go build -o /usr/local/bin/netqa ./cmd/netqa
netqa serve            # collector + dashboard (http://127.0.0.1:8799)
netqa providers        # list configured providers
netqa speedtest        # run one speed test now and record it
```

Data + config live in `~/Library/Application Support/netqa/`
(`netqa.db`, `config.yaml`).

To run 24/7 without the installer, load the launchd agent yourself:

```sh
cp deploy/com.mynetx.netqa.plist ~/Library/LaunchAgents/
launchctl load -w ~/Library/LaunchAgents/com.mynetx.netqa.plist   # unload to stop
```

## First-time setup

On first launch the dashboard opens an **onboarding box** asking for your provider
and its advertised speed:

1. Enter a provider name (e.g. "My ISP") and the down/up target in Mbit, then Save.
2. Under **Networks → provider**, assign your current network to that provider.
3. Data now accrues under it. Click the **Throughput** card any time to force a
   speed test; otherwise tests run automatically whenever the link is idle.
4. When your plan changes (e.g. 40 → 20 Mbit), edit the target — the dashed chart
   line moves; past records keep their original measured values.

## Config (`config.yaml`)

| key | default | meaning |
|---|---|---|
| `targets` | 1.1.1.1, 8.8.8.8, 9.9.9.9 | reachability probe hosts |
| `probe_interval` | 5s | probe cadence |
| `window_size` | 60 | samples in the rolling stats |
| `throughput_check_interval` | 2m | how often an idle window is checked for a test |
| `throughput_min_gap` | 5m | minimum time between automatic speed tests |
| `throughput_idle_mbit` | 3.0 | run a test whenever the link uses less than this (X Mbit) |
| `port` | 8799 | dashboard port |
| `alerts` | true | macOS outage notifications |

## Status / not yet built

Implemented & tested: store, prober, outage classifier, env (VPN-proof gateway
discovery), network identity, idle-aware throughput, config, daemon, dashboard,
notifications, launchd.

Planned next increment: DNS-health collector, traceroute-on-drop capture,
server-side PDF report (CSV export works today), IOKit sleep/wake event recording
(the classifier already excludes asleep gaps via the awake guard).
