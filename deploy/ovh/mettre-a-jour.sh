#!/usr/bin/env bash
# Met a jour et (re)demarre Voice Relay sur la VM : recupere main, lance les
# tests, reconstruit l'image et redemarre les conteneurs. A lancer hors raid.
# Phase 1 : APPLY=false (defaut) verifie et affiche. Phase 2 : APPLY=true.
set -euo pipefail

# --- Variables -------------------------------------------------------------
APPLY="${APPLY:-false}"
BRANCH="main"
# ---------------------------------------------------------------------------

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"

run() {
	if [ "$APPLY" = "true" ]; then
		echo "+ $*"
		"$@"
	else
		echo "[simulation] $*"
	fi
}

echo "== Verifications"
for f in .env Caddyfile docker-compose.yml; do
	[ -f "$f" ] || { echo "Manquant : deploy/ovh/$f" >&2; exit 1; }
done
if [ "$(stat -c %a .env)" != "600" ]; then
	echo "Attention : .env devrait etre en 600 (chmod 600 .env)." >&2
fi
for k in SITE_DOMAIN ACME_EMAIL DISCORD_CLIENT_SECRET DISCORD_OWNER_BOT_TOKEN; do
	grep -Eq "^${k}=.+" .env || { echo "Valeur manquante dans .env : ${k}" >&2; exit 1; }
done
echo "Secrets presents (valeurs non affichees)."

echo "== Code : ${BRANCH}"
run git -C "$ROOT" fetch origin "$BRANCH"
run git -C "$ROOT" checkout "$BRANCH"
run git -C "$ROOT" pull --ff-only origin "$BRANCH"
echo "Version : $(grep -oE 'Fork = "[^"]+"' "$ROOT/internal/version/version.go")"

echo "== Tests (gofmt, vet, tests unitaires)"
run docker build --target test "$ROOT"

echo "== Construction et redemarrage"
run docker compose up -d --build --remove-orphans
run docker compose ps

[ "$APPLY" = "true" ] && echo "Site : https://$(grep -E '^SITE_DOMAIN=' .env | cut -d= -f2-)"
[ "$APPLY" = "true" ] || echo "Simulation terminee : relancer avec APPLY=true ./mettre-a-jour.sh"
