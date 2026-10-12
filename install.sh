#!/bin/bash
# netqa one-shot installer. Builds the binary, installs a launchd agent that
# starts netqa at login (and restarts it if it ever dies), then opens the
# dashboard. Re-run any time to update — it's idempotent.
set -euo pipefail

cd "$(dirname "$0")"

LABEL="com.mynetx.netqa"
BIN_DIR="$HOME/.local/bin"
BIN="$BIN_DIR/netqa"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
PORT=8799
LOGDIR="$HOME/Library/Logs/netqa"

echo "==> Building netqa…"
mkdir -p "$BIN_DIR"
go build -o "$BIN" ./cmd/netqa
echo "    installed: $BIN"

# Pull the configured port if a config already exists.
CFG="$HOME/Library/Application Support/netqa/config.yaml"
if [ -f "$CFG" ]; then
  P=$(grep -E '^port:' "$CFG" | awk '{print $2}' || true)
  [ -n "${P:-}" ] && PORT="$P"
fi

echo "==> Installing launchd agent…"
mkdir -p "$HOME/Library/LaunchAgents" "$LOGDIR"
cat > "$PLIST" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>$LOGDIR/netqa.out.log</string>
  <key>StandardErrorPath</key><string>$LOGDIR/netqa.err.log</string>
</dict>
</plist>
PLISTEOF

# (Re)load so plist changes apply, then restart in place so the new binary runs.
launchctl unload "$PLIST" 2>/dev/null || true
launchctl load -w "$PLIST"
launchctl kickstart -k "gui/$(id -u)/$LABEL" 2>/dev/null || true

echo "==> Waiting for it to come up…"
for i in $(seq 1 15); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    echo "    up on port $PORT"
    break
  fi
  sleep 1
done

cat <<DONE

netqa is running and will auto-start at every login.
  Dashboard:  http://127.0.0.1:$PORT   (open it yourself when you want it)
  Logs:       $LOGDIR/netqa.{out,err}.log  (logs survive reboots, unlike /tmp)
  Update:     make deploy
  Stop/remove: ./uninstall.sh
DONE
