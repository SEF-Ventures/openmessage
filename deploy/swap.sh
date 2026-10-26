#!/usr/bin/env bash
# Move the Google Messages link from gmcli to OpenMessage on one-hel1 (RUNBOOK Phase 2).
# Needs your phone: the QR shows in this terminal; scan it in Google Messages > Device pairing.
# Stops before switching Corpus Sync if the new archive doesn't line up with gmcli's.
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "Run as root."; exit 1; }
cd /opt/openmessage
[ -f docker-compose.yml ] || { echo "Run stage.sh first."; exit 1; }
OLD=/var/lib/gmcli-export/gmessages-gmcli/data
NEW=/var/lib/om-export/gmessages-gmcli/data
KIT=/opt/openmessage/src/deploy

rollback_hint() {
  cat <<'R'
To go back to gmcli:
  cd /opt/openmessage && docker compose down
  systemctl disable --now om-export.timer
  rm -f /etc/systemd/system/corpus-sync@gmessages.service.d/90-openmessage.conf
  systemctl daemon-reload
  systemctl enable --now gmcli-sync.service gmcli-export.timer gmcli-media-fetch.timer
then re-pair gmcli the old way (gmcli-pair). Nothing is deleted either way.
R
}

echo "gmcli newest message (ms): $(jq -r .newest_message_ms "$OLD/state.json" 2>/dev/null || echo unknown)"
read -r -p "This stops gmcli and pairs OpenMessage. Phone ready? Type swap to go: " a
[ "$a" = swap ] || { echo "Stopped. Nothing changed."; exit 1; }

echo "== stopping gmcli"
systemctl disable --now gmcli-sync.service gmcli-export.timer gmcli-media-fetch.timer

echo "== pairing: scan the QR below in Google Messages > Device pairing > QR code scanner"
if ! docker compose run --rm openmessage pair; then
  echo "Pairing didn't finish."; rollback_hint; exit 2
fi

echo "== starting OpenMessage"
docker compose up -d openmessage
cid=$(docker compose ps -q openmessage)
for i in $(seq 1 36); do
  h=$(docker inspect --format '{{.State.Health.Status}}' "$cid" 2>/dev/null || echo starting)
  [ "$h" = healthy ] && break
  sleep 5
done
[ "$h" = healthy ] || { echo "OpenMessage isn't healthy ($h)."; rollback_hint; exit 2; }

echo "== letting history sync for 3 minutes"
sleep 180
systemctl start om-export.service
problem=$(jq -r .problem "$NEW/state.json")
[ "$problem" = null ] || { echo "Export reports problem: $problem"; rollback_hint; exit 2; }

echo "== continuity check (counts only)"
if ! python3 -I - "$OLD/messages.jsonl" "$NEW/messages.jsonl" <<'PY'
import json, sys, time
def load(p):
    out = {}
    for line in open(p, encoding="utf-8"):
        m = json.loads(line)
        out[m["message_id"]] = int(m.get("timestamp_ms") or 0)
    return out
old, new = load(sys.argv[1]), load(sys.argv[2])
week = time.time() * 1000 - 7 * 86400 * 1000
recent = [k for k, t in old.items() if t >= week]
shared = sum(1 for k in recent if k in new)
print(f"gmcli {len(old)}  openmessage {len(new)}  gmcli last 7 days {len(recent)}  of those in openmessage {shared}")
sys.exit(0 if not recent or shared >= 0.5 * len(recent) else 1)
PY
then
  echo "Too few recent messages line up; Corpus Sync would double count. Not switching."
  echo "OpenMessage is left running so this can be looked at."
  rollback_hint; exit 3
fi

echo "== switching Corpus Sync to OpenMessage"
install -D -m 0644 "$KIT/systemd/corpus-sync@gmessages.service.d/90-openmessage.conf" \
  /etc/systemd/system/corpus-sync@gmessages.service.d/90-openmessage.conf
systemctl daemon-reload
systemctl enable --now om-export.timer
systemctl start corpus-sync@gmessages.service || echo "Corpus Sync run reported an error; check: journalctl -u corpus-sync@gmessages -n 50"

echo
echo "Done. Texts now come in through OpenMessage."
echo "Texts inbox: from your computer run"
echo "  ssh -L 7007:127.0.0.1:7007 <your login for this box>"
echo "and open (one use, sets a 30 day cookie):"
echo "  http://localhost:7007/auth/bootstrap?t=$(cat /var/lib/openmessage/data/bootstrap.code)"
