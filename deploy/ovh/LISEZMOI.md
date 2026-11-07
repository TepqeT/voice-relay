# Déploiement sur le VPS (bots + site)

Le VPS fait tourner le bot principal, les bots relais et le site d'administration, en HTTPS via Caddy (certificat Let's Encrypt automatique). Une seule instance.

Prévoir 30 à 45 minutes, hors session.

## Avant de commencer

- Le VPS : Ubuntu 24.04, accès SSH avec sudo, son adresse IP publique.
- Un nom de domaine dont tu gères le DNS (par exemple `relais.ton-domaine.fr`), ou à défaut `<ip-avec-tirets>.sslip.io` (gratuit, sans DNS à gérer).
- Un accès au portail Discord Developer pour l'application "Voice Relay".
- Une seule instance par jeu de tokens : deux instances avec les mêmes tokens répondraient deux fois à chaque commande.

## 1. DNS

Créer un enregistrement `A` : `relais.ton-domaine.fr` vers l'IP de la VM. Vérifier avant de continuer :

```
nslookup relais.ton-domaine.fr
```

## 2. Portail Discord (application Voice Relay)

1. https://discord.com/developers/applications > Voice Relay > **OAuth2**.
2. **Redirects** > Add : `https://relais.ton-domaine.fr/callback` > Save.
3. **Client Secret** > Reset Secret > le copier pour l'étape 5 (il ne sera plus affiché).
4. Onglet **Bot** : laisser **Public Bot** désactivé tant qu'un seul serveur l'utilise. Il faudra l'activer, ainsi que pour chaque bot relais, le jour où d'autres serveurs pourront l'inviter depuis le site.

## 3. Installation de la VM

Le dépôt est privé : la VM n'y a pas encore accès. Copier seulement le script depuis le clone du PC, puis le lancer sur la VM :

```
scp voice-relay\deploy\ovh\installer.sh ubuntu@IP_DE_LA_VM:/tmp/
ssh ubuntu@IP_DE_LA_VM
cd /tmp
sudo bash installer.sh                 # phase 1 : simulation, ne change rien
sudo APPLY=true bash installer.sh      # phase 2 : installe
```

Le script installe Docker et le pare-feu (SSH, 80, 443 seulement), crée le compte système `voicerelay`, génère une **clé de déploiement** et s'arrête pour que tu l'ajoutes :
GitHub > TepqeT/voice-relay > Settings > Deploy keys > Add deploy key, coller la clé affichée, **sans** cocher "Allow write access".

Il clone ensuite le dépôt dans `/opt/voice-relay` et prépare `.env` vide en droits 600.

## 4. Copier les sons personnalisés (facultatif)

Aucun son n'est fourni. Pour un son de début ou de fin de session, déposer des fichiers `.ogg` dans `/opt/voice-relay/deploy/ovh/sounds`, décommenter la ligne `./sounds:/sounds:ro` et `SOUND_ON_START` / `SOUND_ON_STOP` dans `docker-compose.yml`.

## 5. Secrets

```
sudo -u voicerelay nano /opt/voice-relay/deploy/ovh/.env
```

- `SITE_DOMAIN` (sans `https://`), `ACME_EMAIL`, `DISCORD_CLIENT_SECRET` (étape 2).
- Les tokens : `DISCORD_OWNER_BOT_TOKEN` (bot principal) puis `DISCORD_SPEAKER_BOT_TOKEN_1`, `_2`... (un par bot relais, autant que voulu).
- Dons (facultatif) : `PAYPAL_URL` (lien PayPal.me du bouton "Faire un don").
- Copier les valeurs directement d'un fichier à l'autre, sans les coller dans une conversation ni un ticket.

## 6. Bascule

1. Arrêter toute autre instance qui utilise les mêmes tokens.
2. Sur la VM :

```
cd /opt/voice-relay/deploy/ovh
sudo -u voicerelay ./mettre-a-jour.sh               # phase 1 : vérifie les secrets, simule
sudo -u voicerelay APPLY=true ./mettre-a-jour.sh    # phase 2 : tests, construction, démarrage
```

3. Contrôles :
   - `docker compose ps` : deux conteneurs `running` (relay, caddy).
   - `docker compose logs -f relay` : `bot is ready`, puis `web site listening`.
   - `docker compose logs caddy` : `certificate obtained successfully`.
   - Ouvrir `https://relais.ton-domaine.fr` > Se connecter avec Discord > ton serveur.

## Abonnements

- Personne ne crée de compte : chacun se connecte avec Discord. Le site est en français et en anglais (bouton en haut à droite, sinon langue du navigateur).
- L'abonnement est attaché au **compte Discord**, pas à un serveur : son titulaire l'active sur chaque serveur qu'il gère ("Activer mon abonnement ici" dans "Mes serveurs"), autant que le permet le nombre maximum fixé (0 = illimité). Un serveur déjà couvert par un autre compte actif ne peut pas être pris.
- Administrateur : le propriétaire de l'application Discord du bot principal (toi) l'est automatiquement, lien **Administration** en haut du site. Autres administrateurs : `SITE_ADMINS` (identifiants Discord séparés par des virgules).
- Offrir un abonnement : Administration > identifiant Discord du compte (mode développeur activé, clic droit sur la personne > Copier l'identifiant) > **À vie** ou **Jusqu'au** une date > nombre de serveurs maximum > Offrir. Rien à payer, l'abonnement est marqué "offert". La personne se connecte ensuite et l'active sur ses serveurs.
- Suivi des prêts : pour chaque compte, l'administration affiche le nombre de serveurs couverts sur la limite, et pour chaque serveur le nombre de membres, la date d'activation, le nombre de sessions et la dernière session. Un serveur est marqué suspect quand le titulaire n'en est plus membre ou n'y a pas "Gérer le serveur" (abonnement prêté). Les comptes avec des serveurs suspects apparaissent en premier.
- Brider : changer le nombre maximum de serveurs d'un compte (bouton **Brider** : les serveurs déjà activés restent, les nouveaux sont refusés au-delà), retirer un serveur précis (**Retirer ce serveur**), ou retirer tout l'abonnement.
- Retirer : bouton **Retirer**. Les serveurs restent associés au compte et le bot annonce que l'abonnement a pris fin.
- Les abonnements et les serveurs activés sont dans `deploy/ovh/data/subscriptions.json` (à sauvegarder avec `data`).
- Blocage des sessions : désactivé par défaut. Une fois `SUBSCRIPTION_REQUIRED: "true"` activé, un lancement de session sur un serveur non couvert affiche, dans la langue du serveur : pas d'abonnement, abonnement expiré le jj/mm/aaaa, ou abonnement terminé, avec le lien du site. Avant d'activer : t'offrir un abonnement à vie et l'activer sur ton serveur depuis le site.
- Depuis v62, Voice Relay est gratuit : le site ne vend plus d'abonnement et n'en parle plus tant que `SUBSCRIPTION_REQUIRED` n'est pas à `true`. Les abonnements donnés depuis l'administration restent possibles.
- Dons sur le site (PayPal Checkout) : le visiteur, connecté ou non, choisit un montant et paie dans la fenêtre PayPal, avec un compte PayPal ou une carte. Un don ne débloque rien. L'argent arrive sur le compte PayPal propriétaire de l'application et le don apparaît dans Administration > Dons reçus (fichier `data/payments.jsonl`, à sauvegarder).
  1. https://developer.paypal.com > Apps & Credentials > Create App (type Merchant). Un compte PayPal Business est nécessaire pour les identifiants Live.
  2. Dans `.env` : `PAYPAL_CLIENT_ID`, `PAYPAL_CLIENT_SECRET`, `PAYPAL_MODE=sandbox`. Montants proposés : `DONATION_AMOUNTS` (défaut `3,5,10,20`).
  3. Relancer `mettre-a-jour.sh`, donner avec un compte de test sandbox, vérifier Administration > Dons reçus.
  4. Passer aux identifiants de l'onglet Live et `PAYPAL_MODE=live`, relancer.
- Sans identifiants PayPal : le lien `PAYPAL_URL` sert de bouton "Faire un don".
- Support : bouton "Contacter le support" en bas de chaque page, vers ton profil Discord (propriétaire de l'application) ; `SUPPORT_URL` pour un autre lien.
- Désinstaller : page du serveur > Désinstaller (case à cocher). Arrête la session, supprime les salons, fait partir le bot principal et les bots relais, libère l'abonnement. Les rôles restent.

## Mettre à jour plus tard

```
cd /opt/voice-relay/deploy/ovh && sudo -u voicerelay APPLY=true ./mettre-a-jour.sh
```

Le script récupère `main`, lance les tests, reconstruit et redémarre. À faire hors session.

## Sauvegarde

À sauvegarder : `deploy/ovh/data` et `.env`. Le reste se reconstruit depuis le dépôt.

## En cas de problème

D'abord : Administration > Journal des installations (ou la section Problèmes récents de la page du serveur). Chaque tentative d'installation ratée y est notée avec sa raison (permissions manquantes, rôle au-dessus du bot, salon refusé, connexion Discord refusée, paiement échoué). Dans les logs : `docker compose logs relay | grep setup:`.


| Symptôme | Cause probable | Action |
|---|---|---|
| Caddy n'obtient pas de certificat | DNS pas encore propagé, ou ports 80/443 fermés | `nslookup`, pare-feu OVH (Network > Firewall) en plus de `ufw` |
| "Invalid OAuth2 redirect_uri" sur Discord | Redirection absente ou différente | Étape 2 : l'adresse doit être exactement `https://SITE_DOMAIN/callback` |
| Site : "Discord n'a pas répondu correctement" | Client Secret faux ou réinitialisé depuis | Refaire Reset Secret, mettre à jour `.env`, relancer |
| Commandes en double sur Discord | Une autre instance tourne avec les mêmes tokens | L'arrêter |
| Le site affiche "Bot absent" pour ton serveur | Le bot n'a pas fini de démarrer | Attendre `bot is ready` puis recharger |

## Limites de cette version du site

- Les noms des rôles créés sont ceux du compose (`SPEAKER_ROLE`, `SQUAD_LEAD_ROLE`, `ACCESS_ROLE`, `BOARD_ROLE`, `CLASS_ROLES`). Les modifier depuis le site viendra avec la configuration en base.
- Sessions en mémoire : un redémarrage déconnecte les administrateurs, qui se reconnectent en un clic.
