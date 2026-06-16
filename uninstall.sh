#!/bin/bash
# Stop netqa and remove its launchd agent. Leaves your data (the SQLite evidence)
# untouched in ~/Library/Application Support/netqa/.
set -euo pipefail

LABEL="com.mynetx.netqa"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

if [ -f "$PLIST" ]; then
  launchctl unload "$PLIST" 2>/dev/null || true
  rm -f "$PLIST"
  echo "netqa stopped and removed from login items."
else
  echo "netqa agent not installed."
fi

echo "Binary: $HOME/.local/bin/netqa (delete manually if you want it gone)"
echo "Data kept: $HOME/Library/Application Support/netqa/"
