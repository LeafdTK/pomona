#!/bin/sh
# Run Pomona at login on a Mac, so the 07:00 brief is written whether or not
# the browser is open. Builds the binary, writes a launchd agent, loads it.
#
#   sh dev/install-service.sh            install or update
#   sh dev/install-service.sh remove     stop and remove
set -e

label="dev.leafd.pomona"
agent="$HOME/Library/LaunchAgents/$label.plist"
binary="$HOME/.pomona/bin/pomona"
here="$(cd "$(dirname "$0")/.." && pwd)"

if [ "$1" = "remove" ]; then
  launchctl bootout "gui/$(id -u)" "$agent" 2>/dev/null || true
  rm -f "$agent"
  echo "removed $label"
  exit 0
fi

mkdir -p "$HOME/.pomona/bin" "$HOME/Library/LaunchAgents"
(cd "$here" && go build -o "$binary" ./server)

sed -e "s#__BINARY__#$binary#g" -e "s#__HOME__#$HOME#g" "$here/dev/launchd/$label.plist" > "$agent"
launchctl bootout "gui/$(id -u)" "$agent" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$agent"

echo "$label is running at http://127.0.0.1:7777 and will start at login."
echo "logs: ~/.pomona/pomona.log"
