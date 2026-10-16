#!/usr/bin/env bash
# Builds aptbot and installs it as a LaunchAgent (runs every hour).
# Usage: ./deploy/install-mac.sh                # immowelt only (default)
#        ONLY=immowelt,immoscout ./deploy/install-mac.sh
set -euo pipefail
DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

command -v go >/dev/null || { echo "Go not found: brew install go"; exit 1; }
[ -f config.json ] || { echo "config.json missing: cp config.example.json config.json and edit it"; exit 1; }

go build -o bin/aptbot ./cmd/aptbot

PLIST="$HOME/Library/LaunchAgents/de.aptbot.plist"
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
sed -e "s#__DIR__#$DIR#g" -e "s#__HOME__#$HOME#g" \
    -e "s#__ONLY__#${ONLY:-immowelt}#g" \
    deploy/de.aptbot.plist > "$PLIST"
chmod 600 "$PLIST"

launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"
echo "Installed. First run starting now; follow it with:  tail -f ~/Library/Logs/aptbot.log"
echo "Uninstall: launchctl bootout gui/$(id -u) $PLIST && rm $PLIST"
