#!/usr/bin/env bash
# Stage OpenMessage on one-hel1 next to gmcli. Switches nothing: gmcli keeps the Google link and
# Corpus Sync keeps reading gmcli's export. Safe to run more than once.
#
#   git clone -b sef/read-draft-only https://github.com/SEF-Ventures/openmessage /opt/openmessage/src
#   bash /opt/openmessage/src/deploy/stage.sh
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "Run as root."; exit 1; }
SRC=/opt/openmessage/src
KIT="$SRC/deploy"
[ -d "$KIT" ] || { echo "Clone the fork to $SRC first."; exit 1; }
getent group corpus >/dev/null || { echo "Group corpus is missing; is this the One box?"; exit 1; }

echo "== user and folders"
getent group openmessage >/dev/null || groupadd --system --gid 10010 openmessage
id openmessage >/dev/null 2>&1 || useradd --system --uid 10010 --gid 10010 \
  --home-dir /var/lib/openmessage --shell /usr/sbin/nologin openmessage
install -d -o openmessage -g openmessage -m 0700 /var/lib/openmessage /var/lib/openmessage/data
install -d -o openmessage -g corpus -m 2750 /var/lib/om-export /var/lib/om-export/gmessages-gmcli \
  /var/lib/om-export/gmessages-gmcli/data /var/lib/om-export/gmessages-gmcli/media

echo "== files"
git -C "$SRC" rev-parse HEAD > /opt/openmessage/PIN
install -m 0644 "$KIT/compose/docker-compose.yml" "$KIT/compose/Caddyfile" /opt/openmessage/
install -D -m 0755 "$KIT/export/om-export" /opt/openmessage/bin/om-export
install -m 0644 "$KIT/systemd/om-export.service" "$KIT/systemd/om-export.timer" /etc/systemd/system/
systemctl daemon-reload   # installs the units; enables nothing

echo "== checks"
python3 -I "$KIT/export/test_om_export.py"
cd /opt/openmessage
docker compose config -q
echo "== build (a few minutes)"
docker compose build openmessage

echo "== still on gmcli"
systemctl is-active gmcli-sync.service gmcli-export.timer gmcli-media-fetch.timer || true
echo "Staged at $(cut -c1-12 /opt/openmessage/PIN). Nothing switched. Next, when you have your phone and a minute:"
echo "  bash $KIT/swap.sh"
