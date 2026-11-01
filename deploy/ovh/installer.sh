#!/usr/bin/env bash
# Installation initiale de Voice Relay sur un VPS Ubuntu 24.04 (paquet docker-compose-v2 d'Ubuntu).
# Phase 1 : APPLY=false (defaut) affiche ce qui serait fait, sans rien changer.
# Phase 2 : sudo APPLY=true ./installer.sh
# Aucun secret ici : les tokens vont dans .env, rempli a la main.
set -euo pipefail

# --- Variables -------------------------------------------------------------
APPLY="${APPLY:-false}"
REPO_SSH="git@github.com:TepqeT/voice-relay.git"   # depot prive : cle de deploiement en lecture seule
INSTALL_DIR="/opt/voice-relay"
RUN_USER="voicerelay"                                # compte systeme qui possede les fichiers
SSH_PORT="22"
# ---------------------------------------------------------------------------

run() {
	if [ "$APPLY" = "true" ]; then
		echo "+ $*"
		"$@"
	else
		echo "[simulation] $*"
	fi
}

if [ "$APPLY" = "true" ] && [ "$(id -u)" -ne 0 ]; then
	echo "Lancer avec sudo en phase APPLY=true." >&2
	exit 1
fi

echo "== 1. Paquets : Docker, Compose, Git, pare-feu"
run apt-get update
run apt-get install -y docker.io docker-compose-v2 git ufw ca-certificates
run systemctl enable --now docker

echo "== 2. Pare-feu : SSH, HTTP, HTTPS uniquement (les bots n'ont besoin d'aucun port entrant)"
run ufw allow "${SSH_PORT}/tcp"
run ufw allow 80/tcp
run ufw allow 443/tcp
run ufw allow 443/udp
run ufw --force enable

echo "== 3. Compte systeme ${RUN_USER}"
if ! id "$RUN_USER" >/dev/null 2>&1; then
	run useradd --system --create-home --shell /usr/sbin/nologin "$RUN_USER"
fi
run usermod -aG docker "$RUN_USER"

echo "== 4. Cle de deploiement GitHub (lecture seule)"
KEY="/home/${RUN_USER}/.ssh/id_ed25519"
if [ ! -f "$KEY" ]; then
	run install -d -m 700 -o "$RUN_USER" -g "$RUN_USER" "/home/${RUN_USER}/.ssh"
	run sudo -u "$RUN_USER" ssh-keygen -t ed25519 -N "" -C "voice-relay-ovh" -f "$KEY"
fi
if [ "$APPLY" = "true" ]; then
	echo "Ajouter cette cle publique dans GitHub : depot TepqeT/voice-relay > Settings > Deploy keys > Add (sans Allow write access) :"
	cat "${KEY}.pub"
	read -r -p "Entree une fois la cle ajoutee..." _
	sudo -u "$RUN_USER" ssh -o StrictHostKeyChecking=accept-new -T git@github.com || true
fi

echo "== 5. Clone du depot dans ${INSTALL_DIR}"
if [ ! -d "${INSTALL_DIR}/.git" ]; then
	run install -d -o "$RUN_USER" -g "$RUN_USER" "$INSTALL_DIR"
	run sudo -u "$RUN_USER" git clone "$REPO_SSH" "$INSTALL_DIR"
fi

echo "== 6. Fichiers de secrets a remplir (droits 600)"
OVH="${INSTALL_DIR}/deploy/ovh"
if [ ! -f "${OVH}/.env" ]; then
	run sudo -u "$RUN_USER" install -m 600 "${OVH}/.env.example" "${OVH}/.env"
fi

echo
echo "Suite : remplir ${OVH}/.env, puis :"
echo "  cd ${OVH} && sudo -u ${RUN_USER} APPLY=false ./mettre-a-jour.sh"
echo "  cd ${OVH} && sudo -u ${RUN_USER} APPLY=true ./mettre-a-jour.sh"
[ "$APPLY" = "true" ] || echo "Simulation terminee : relancer avec sudo APPLY=true ./installer.sh"
