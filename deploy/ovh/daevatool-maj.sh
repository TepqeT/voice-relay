#!/usr/bin/env bash
# Mise en place, une seule fois, du depot des mises a jour de DaevaTool sur la VM.
# - dossier /srv/daevatool-maj/<chemin aleatoire> (rien d'autre dans /srv/daevatool-maj), servi par Caddy sous https://<SITE_DOMAIN>/maj/<chemin>/
# - compte daevatool-maj : sftp seulement, par une cle reservee a GitHub Actions
# - cle de signature ECDSA P-256 : la partie privee va dans les secrets GitHub, puis est effacee de la VM
# Ecrit les valeurs a recopier dans GitHub dans SECRETS_FILE (root seulement, jamais affichees :
# un agent qui lance le script ne voit pas les cles privees). L'admin l'affiche lui-meme, puis l'efface.
# Phase 1 : APPLY=false (defaut) verifie et affiche. Phase 2 : sudo APPLY=true ./daevatool-maj.sh
# Relance : garde le chemin et les cles ; NOUVELLES_CLES=true pour les refaire.
set -euo pipefail

# --- Variables -------------------------------------------------------------
APPLY="${APPLY:-false}"
NOUVELLES_CLES="${NOUVELLES_CLES:-false}"
BASE="/srv/daevatool-maj"
MAJ_USER="daevatool-maj"
MAJ_HOME="/home/daevatool-maj"          # hors du dossier servi : la cle publique et le shell n'y sont pas exposes
CHEMIN_FILE="/etc/daevatool-maj.chemin"
SECRETS_FILE="/root/daevatool-secrets.txt"
VR_DIR="/opt/voice-relay"
VR_USER="voicerelay"
# ---------------------------------------------------------------------------

cd "$(dirname "$0")"

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
[ -f .env ] || { echo "Manquant : deploy/ovh/.env" >&2; exit 1; }
DOMAIN="$(grep -E '^SITE_DOMAIN=' .env | cut -d= -f2-)"
[ -n "$DOMAIN" ] || { echo "SITE_DOMAIN vide dans .env" >&2; exit 1; }
grep -q 'handle_path /maj/' Caddyfile || { echo "Caddyfile sans la route /maj/ : faire d'abord git pull du depot voice-relay" >&2; exit 1; }
command -v openssl >/dev/null || { echo "openssl absent" >&2; exit 1; }
SFTP_SERVER="$(for p in /usr/lib/openssh/sftp-server /usr/libexec/openssh/sftp-server; do [ -x "$p" ] && echo "$p" && break; done)"
[ -n "$SFTP_SERVER" ] || { echo "sftp-server introuvable" >&2; exit 1; }
echo "Domaine : $DOMAIN"

if [ -f "$CHEMIN_FILE" ]; then CHEMIN="$(cat "$CHEMIN_FILE")"; else CHEMIN="$(openssl rand -hex 16)"; fi
DIR="$BASE/$CHEMIN"
echo "Dossier : $DIR"

echo "== Compte $MAJ_USER et dossier"
if ! id "$MAJ_USER" >/dev/null 2>&1; then
	run useradd --system --home-dir "$MAJ_HOME" --create-home --shell /bin/sh "$MAJ_USER"
	run passwd -l "$MAJ_USER"
fi
run install -d -m 755 -o root -g root "$BASE"
run install -d -m 755 -o "$MAJ_USER" -g "$MAJ_USER" "$DIR"
run install -d -m 755 -o "$MAJ_USER" -g "$MAJ_USER" "$MAJ_HOME"
run install -d -m 700 -o "$MAJ_USER" -g "$MAJ_USER" "$MAJ_HOME/.ssh"
[ "$APPLY" = "true" ] && echo "$CHEMIN" > "$CHEMIN_FILE" && chmod 600 "$CHEMIN_FILE"

echo "== Caddy : route /maj/ (redemarre seulement le conteneur caddy)"
run docker compose up -d --no-deps caddy          # --no-deps : les bots ne sont ni recrees ni redemarres

if [ "$APPLY" != "true" ]; then
	echo "Simulation terminee : relancer avec sudo APPLY=true ./daevatool-maj.sh"
	exit 0
fi

AK="$MAJ_HOME/.ssh/authorized_keys"
if [ -s "$AK" ] && [ "$NOUVELLES_CLES" != "true" ]; then
	echo "Cles deja creees (NOUVELLES_CLES=true pour les refaire). URL : https://$DOMAIN/maj/$CHEMIN/"
	exit 0
fi

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
ssh-keygen -q -t ed25519 -N "" -C "github-actions-daevatool" -f "$TMP/deploy"
echo "restrict,command=\"$SFTP_SERVER\" $(cat "$TMP/deploy.pub")" > "$AK"
chown "$MAJ_USER:$MAJ_USER" "$AK"; chmod 600 "$AK"
openssl ecparam -name prime256v1 -genkey -noout -out "$TMP/sign.pem" 2>/dev/null
PUB="$(openssl ec -in "$TMP/sign.pem" -pubout -outform DER 2>/dev/null | base64 -w0)"
KH="$(for k in /etc/ssh/ssh_host_*_key.pub; do echo "$DOMAIN $(cut -d' ' -f1,2 "$k")"; done)"

umask 077
cat > "$SECRETS_FILE" <<FIN

================================================================
A recopier dans GitHub : depot TepqeT/DaevaTool > Settings
> Secrets and variables > Actions. Ne les colle nulle part ailleurs.
================================================================
Onglet Secrets, "New repository secret" :

UPDATE_URL
https://$DOMAIN/maj/$CHEMIN/

VPS_HOST
$DOMAIN

VPS_USER
$MAJ_USER

VPS_KNOWN_HOSTS
$KH

VPS_SSH_KEY
$(cat "$TMP/deploy")

SIGNING_KEY
$(cat "$TMP/sign.pem")

Onglet Variables, "New repository variable" (pas secret) :

UPDATE_PUBKEY
$PUB
================================================================
Effacer ce fichier apres usage : sudo shred -u $SECRETS_FILE
Premier telechargement de l'outil (apres la 1re publication) :
https://$DOMAIN/maj/$CHEMIN/DaevaTool.exe
FIN
chmod 600 "$SECRETS_FILE"
echo "Valeurs ecrites dans $SECRETS_FILE (root seulement). A afficher par l'admin lui-meme :"
echo "  sudo cat $SECRETS_FILE"
echo "puis, une fois recopiees dans GitHub :"
echo "  sudo shred -u $SECRETS_FILE"
echo "Adresse publique (sans secret) : https://$DOMAIN/maj/<chemin>/ ; variable UPDATE_PUBKEY : $PUB"
