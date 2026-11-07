#!/usr/bin/env bash
# Mise a jour automatique de Voice Relay sur la VM, lancee par cron toutes
# les 30 minutes avec le compte voicerelay : si origin/main a avance, lance
# mettre-a-jour.sh (tests, construction, redemarrage). Ne fait rien pendant
# une session vocale (salons de session presents dans data/rvr-channels.json),
# la mise a jour attend le prochain passage.
# Journal : deploy/ovh/logs/maj-auto.log (hors depot).
#
# Installation du cron (une fois, en root) :
#   sudo -u voicerelay INSTALLER=true /opt/voice-relay/deploy/ovh/maj-auto.sh
# Retrait : sudo -u voicerelay crontab -r
set -euo pipefail

# --- Variables -------------------------------------------------------------
INSTALLER="${INSTALLER:-false}"
BRANCH="main"
INTERVALLE="*/30 * * * *"
# ---------------------------------------------------------------------------

export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
cd "$(dirname "$0")"
OVH="$(pwd)"
ROOT="$(cd ../.. && pwd)"
LOG_DIR="$OVH/logs"
LOG="$LOG_DIR/maj-auto.log"
LOCK="$OVH/logs/maj-auto.lock"
SESSION_FILE="$OVH/data/rvr-channels.json"

mkdir -p "$LOG_DIR"

if [ "$INSTALLER" = "true" ]; then
	ligne="$INTERVALLE $OVH/maj-auto.sh >> $LOG 2>&1"
	# Sans crontab existant, crontab -l et grep echouent : ce n'est pas une erreur.
	actuel="$(crontab -l 2>/dev/null || true)"
	reste="$(printf '%s\n' "$actuel" | grep -v "maj-auto.sh" || true)"
	printf '%s\n%s\n' "$reste" "$ligne" | sed '/^$/d' | crontab -
	echo "Cron installe pour $(id -un) :"
	crontab -l
	exit 0
fi

# Un seul passage a la fois (une construction dure 10 a 15 minutes).
exec 9>"$LOCK"
flock -n 9 || exit 0

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*"; }

git -C "$ROOT" fetch -q origin "$BRANCH"
local_rev="$(git -C "$ROOT" rev-parse HEAD)"
remote_rev="$(git -C "$ROOT" rev-parse "origin/$BRANCH")"
[ "$local_rev" != "$remote_rev" ] || exit 0

if [ -f "$SESSION_FILE" ] && grep -q '"category"' "$SESSION_FILE"; then
	log "nouvelle version ${remote_rev:0:7} disponible, session en cours : report"
	exit 0
fi

log "mise a jour ${local_rev:0:7} -> ${remote_rev:0:7}"
if APPLY=true "$OVH/mettre-a-jour.sh"; then
	log "mise a jour terminee : $(grep -oE 'Fork = "[^"]+"' "$ROOT/internal/version/version.go")"
else
	log "ECHEC de la mise a jour (voir ci-dessus), l'ancienne version reste en place"
	exit 1
fi
