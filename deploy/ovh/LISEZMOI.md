# Déploiement sur la VM OVH (bots + site)

La VM fait tourner les bots des deux factions et le site d'administration, en HTTPS via Caddy (certificat Let's Encrypt automatique).

Prévoir 30 à 45 minutes, hors raid.

## Avant de commencer

- La VM OVH : Ubuntu 24.04 ou Debian 12, accès SSH avec sudo, son adresse IP publique.
- Un nom de domaine dont tu gères le DNS (par exemple `relais.ton-domaine.fr`).
- Un accès au portail Discord Developer pour l'application "Voix de Pandaemonium".
- Arrêter le déploiement du PC au moment de lancer la VM, pas avant (étape 7). Deux instances avec les mêmes tokens répondraient deux fois à chaque commande.

## 1. DNS

Créer un enregistrement `A` : `relais.ton-domaine.fr` vers l'IP de la VM. Vérifier avant de continuer :

```
nslookup relais.ton-domaine.fr
```

## 2. Portail Discord (application Voix de Pandaemonium)

1. https://discord.com/developers/applications > Voix de Pandaemonium > **OAuth2**.
2. **Redirects** > Add : `https://relais.ton-domaine.fr/callback` > Save.
3. **Client Secret** > Reset Secret > le copier pour l'étape 5 (il ne sera plus affiché).
4. Onglet **Bot** : laisser **Public Bot** désactivé tant que seul Aion 2 France l'utilise. Il faudra l'activer, ainsi que pour chaque Héraut, le jour où d'autres serveurs pourront l'inviter depuis le site.

## 3. Installation de la VM

Le dépôt est privé : la VM n'y a pas encore accès. Copier seulement le script depuis le clone du PC, puis le lancer sur la VM :

```
scp C:\Users\toi\Documents\DaevaTool\aion-relay\voice-relay\deploy\ovh\installer.sh ubuntu@IP_DE_LA_VM:/tmp/
ssh ubuntu@IP_DE_LA_VM
cd /tmp
sudo bash installer.sh                 # phase 1 : simulation, ne change rien
sudo APPLY=true bash installer.sh      # phase 2 : installe
```

Le script installe Docker et le pare-feu (SSH, 80, 443 seulement), crée le compte système `voicerelay`, génère une **clé de déploiement** et s'arrête pour que tu l'ajoutes :
GitHub > TepqeT/voice-relay > Settings > Deploy keys > Add deploy key, coller la clé affichée, **sans** cocher "Allow write access".

Il clone ensuite le dépôt dans `/opt/voice-relay` et prépare `.env` et `.env.elyos` vides en droits 600.

## 4. Copier les sons personnalisés (facultatif)

Si les annonces vocales générées sur le PC doivent être gardées, copier le dossier `C:\Users\toi\source\aion-relay\sounds` vers `/opt/voice-relay/deploy/ovh/sounds` (WinSCP ou `scp`), puis décommenter les lignes `./sounds:/sounds:ro` dans `docker-compose.yml`. Sans cela, les sons par défaut de l'image sont utilisés.

## 5. Secrets

```
sudo -u voicerelay nano /opt/voice-relay/deploy/ovh/.env
sudo -u voicerelay nano /opt/voice-relay/deploy/ovh/.env.elyos
```

- `.env` : `SITE_DOMAIN` (sans `https://`), `ACME_EMAIL`, `DISCORD_CLIENT_SECRET` (étape 2), puis les tokens Asmodiens repris du `.env` du PC.
- `.env.elyos` : les tokens Élyséens repris du `.env.elyos` du PC.
- Copier les valeurs directement d'un fichier à l'autre, sans les coller dans une conversation ni un ticket.

## 6. Reprendre l'état du PC (facultatif mais conseillé)

Pour garder les réglages `/setup` (rôles, salons, Hérauts actifs), copier `C:\Users\toi\source\aion-relay\data\store.yaml` vers `/opt/voice-relay/deploy/ovh/data/store.yaml` et `data-elyos\store.yaml` vers `deploy/ovh/data-elyos/store.yaml`, puis :

```
sudo chown -R voicerelay:voicerelay /opt/voice-relay/deploy/ovh/data /opt/voice-relay/deploy/ovh/data-elyos
```

## 7. Bascule

1. Sur le PC, arrêter le relais : `docker compose --profile elyos down` dans `C:\Users\toi\source\aion-relay`.
2. Sur la VM :

```
cd /opt/voice-relay/deploy/ovh
sudo -u voicerelay ./mettre-a-jour.sh               # phase 1 : vérifie les secrets, simule
sudo -u voicerelay APPLY=true ./mettre-a-jour.sh    # phase 2 : tests, construction, démarrage
```

3. Contrôles :
   - `docker compose --profile elyos ps` : trois conteneurs `running` (asmos, elyos, caddy).
   - `docker compose logs -f asmos` : `bot is ready`, puis `web site listening`.
   - `docker compose logs caddy` : `certificate obtained successfully`.
   - Ouvrir `https://relais.ton-domaine.fr` > Se connecter avec Discord > Aion 2 France.

## Mettre à jour plus tard

```
cd /opt/voice-relay/deploy/ovh && sudo -u voicerelay APPLY=true ./mettre-a-jour.sh
```

Le script récupère `main`, lance les tests, reconstruit et redémarre. À faire hors raid.

## Sauvegarde

À sauvegarder : `deploy/ovh/data`, `deploy/ovh/data-elyos`, `.env`, `.env.elyos`. Le reste se reconstruit depuis le dépôt.

## En cas de problème

| Symptôme | Cause probable | Action |
|---|---|---|
| Caddy n'obtient pas de certificat | DNS pas encore propagé, ou ports 80/443 fermés | `nslookup`, pare-feu OVH (Network > Firewall) en plus de `ufw` |
| "Invalid OAuth2 redirect_uri" sur Discord | Redirection absente ou différente | Étape 2 : l'adresse doit être exactement `https://SITE_DOMAIN/callback` |
| Site : "Discord n'a pas répondu correctement" | Client Secret faux ou réinitialisé depuis | Refaire Reset Secret, mettre à jour `.env`, relancer |
| Commandes en double sur Discord | Le PC tourne encore | Arrêter le relais du PC |
| Le site affiche "Bot absent" pour Aion 2 France | Le bot n'a pas fini de démarrer | Attendre `bot is ready` puis recharger |

## Limites de cette première version du site

- Le site sert le bot Asmodiens (Voix de Pandaemonium). Les Élyséens gardent la configuration du compose ; un second site ou un bot principal unique viendront avec la configuration par serveur (`docs/saas-plan.md`, lot 1).
- Les noms des rôles créés sont ceux du compose (`RAID_LEAD_ROLE`, `SQUAD_LEAD_ROLE`, `FACTION_ROLE`, `ACCESS_ROLE`, `BOARD_ROLE`, `CLASS_ROLES`). Les modifier depuis le site viendra avec la configuration en base.
- Sessions en mémoire : un redémarrage déconnecte les administrateurs, qui se reconnectent en un clic.
