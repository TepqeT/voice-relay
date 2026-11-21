#!/usr/bin/env bash
# Arret definitif de Voice Relay sur la VM, apres la v71 (qui a deja fait
# partir les bots de tous les serveurs Discord).
# Garde : Caddy, son certificat, la route /maj/ de DaevaTool, le compte
# voicerelay, deploy/ovh (Caddyfile, compose, .env reduit, daevatool-maj.sh).
# Retire : cron de mise a jour, conteneur et image du bot, cache de build
# Docker, donnees et logs du bot, code source, tokens et secrets de .env.
# Phase 1 : sudo APPLY=false ./arreter-voice-relay.sh (affiche seulement)
# Phase 2 : sudo APPLY=true ./arreter-voice-relay.sh
set -euo pipefail

# --- Variables -------------------------------------------------------------
APPLY="${APPLY:-false}"
VR_DIR="/opt/voice-relay"
OVH="$VR_DIR/deploy/ovh"
RUN_USER="voicerelay"
RELAY_CONTAINER="voice-relay"
CADDY_CONTAINER="voice-relay-caddy"
RELAY_IMAGE="voice-relay:local"
# ---------------------------------------------------------------------------

run() {
	if [ "$APPLY" = "true" ]; then
		echo "+ $*"
		"$@"
	else
		echo "[simulation] $*"
	fi
}

echo "== Verifications"
[ "$(id -u)" = "0" ] || { echo "A lancer avec sudo" >&2; exit 1; }
cd "$OVH"
for f in arret/Caddyfile arret/docker-compose.yml .env Caddyfile daevatool-maj.sh; do
	[ -f "$f" ] || { echo "Manquant : $OVH/$f (faire d'abord git pull)" >&2; exit 1; }
done
grep -q 'handle_path /maj/' arret/Caddyfile || { echo "arret/Caddyfile sans la route /maj/ : arret" >&2; exit 1; }
for k in SITE_DOMAIN ACME_EMAIL; do
	grep -Eq "^${k}=.+" .env || { echo "Valeur manquante dans .env : ${k}" >&2; exit 1; }
done
if docker ps --format '{{.Names}}' | grep -qx "$RELAY_CONTAINER"; then
	echo "Bot en marche. Desinstallations faites par la v71 :"
	docker logs "$RELAY_CONTAINER" 2>&1 | grep -c 'shutdown_uninstalled' || true
fi
echo "Disque avant : $(df -h / | tail -1)"

echo "== 1. Cron de mise a jour automatique"
if crontab -u "$RUN_USER" -l 2>/dev/null | grep -q 'maj-auto.sh'; then
	if [ "$APPLY" = "true" ]; then
		rest="$(crontab -u "$RUN_USER" -l 2>/dev/null | grep -v 'maj-auto.sh' || true)"
		if [ -n "$rest" ]; then printf '%s\n' "$rest" | crontab -u "$RUN_USER" -; else crontab -u "$RUN_USER" -r; fi
		echo "+ ligne maj-auto.sh retiree du crontab de $RUN_USER"
	else
		echo "[simulation] retirer la ligne maj-auto.sh du crontab de $RUN_USER"
	fi
else
	echo "Pas de cron maj-auto.sh"
fi

echo "== 2. Conteneur du bot"
if docker ps -a --format '{{.Names}}' | grep -qx "$RELAY_CONTAINER"; then
	run docker stop "$RELAY_CONTAINER"
	run docker rm "$RELAY_CONTAINER"
else
	echo "Pas de conteneur $RELAY_CONTAINER"
fi

echo "== 3. Caddy : page Projet arrete, /maj/ conservee (rechargement a chaud)"
# cp et non mv : le Caddyfile est monte dans le conteneur, il faut garder le meme fichier.
run cp arret/Caddyfile Caddyfile
run cp arret/docker-compose.yml docker-compose.yml
run docker exec "$CADDY_CONTAINER" caddy reload --config /etc/caddy/Caddyfile

echo "== 4. Image du bot et cache de build Docker"
if docker image inspect "$RELAY_IMAGE" >/dev/null 2>&1; then
	run docker image rm "$RELAY_IMAGE"
fi
run docker image prune -f
run docker builder prune -af

echo "== 5. Donnees et logs du bot"
for d in data logs; do
	[ -e "$d" ] && run rm -rf "$OVH/$d"
done

echo "== 6. .env : on ne garde que SITE_DOMAIN et ACME_EMAIL (valeurs non affichees)"
if [ "$APPLY" = "true" ]; then
	grep -E '^(SITE_DOMAIN|ACME_EMAIL)=' .env > .env.new
	chmod 600 .env.new
	chown "$RUN_USER:$RUN_USER" .env.new
	mv .env.new .env
	echo "+ .env reduit a $(wc -l < .env) ligne(s)"
else
	echo "[simulation] reduire .env a SITE_DOMAIN et ACME_EMAIL ($(grep -c . .env) ligne(s) aujourd'hui)"
fi

echo "== 7. Code source du bot (deploy/ovh est garde)"
for p in "$VR_DIR"/* "$VR_DIR"/.[!.]*; do
	[ -e "$p" ] || continue
	[ "$p" = "$VR_DIR/deploy" ] && continue
	run rm -rf "$p"
done
for p in "$VR_DIR"/deploy/*; do
	[ "$p" = "$OVH" ] && continue
	run rm -rf "$p"
done
run rm -rf "$OVH/arret"
for f in maj-auto.sh mettre-a-jour.sh installer.sh LISEZMOI.md .env.example; do
	[ -e "$OVH/$f" ] && run rm -f "$OVH/$f"
done

echo "== Etat final"
docker ps --format '{{.Names}} {{.Status}}'
echo "Disque apres : $(df -h / | tail -1)"
[ "$APPLY" = "true" ] || echo "Simulation terminee : relancer avec sudo APPLY=true ./arreter-voice-relay.sh"
