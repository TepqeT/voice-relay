# Journal des versions

La version courante est dans `internal/version/version.go` (`Fork`). Une entrée par livraison.

## v73

- server A : panneau dans le salon existant `bot-commands` (`PANEL_CHANNEL`), salons de session dans la catégorie existante `VOICE` (`CATEGORY`). Le panneau reste en anglais (langue du serveur).
- Changement de nom de salon ou de catégorie dans la configuration : le bot supprime ceux qu'il avait créés sous l'ancien nom et reprend ceux qui existent sous le nouveau, sans les modifier ni jamais les supprimer (même à la désinstallation).

## v72

- Un seul serveur : Voice Relay ne sert plus que le serveur 100000000000000072. Il quitte tous les autres au démarrage (même désinstallation que le site, rôles conservés) et tout serveur qui l'ajoute. Sur ce serveur tout fonctionne comme avant : rôles créés à l'arrivée du bot, panneau, sessions. Le site public reste sur "Projet arrêté" ; les invitations passent par des liens directs.
- Script de ménage du VPS (`arreter-voice-relay.sh`) retiré : il supprimait le bot et ses secrets.

## v71

- Projet arrêté le 21/11/2026. Au démarrage, Voice Relay se désinstalle de chaque serveur : session arrêtée, panneau et salons supprimés, bots relais puis bot principal partis, rôles conservés. Un serveur qui réinvite le bot est quitté aussitôt. Le site n'affiche plus que "Projet arrêté" (fr, en) ; `/healthz` et les fichiers statiques restent servis. DaevaTool (`/maj/`) n'est pas concerné.

## v70

- Installation plus courte : un seul bot relais suffit. Le site ne réclame plus les 11 relais (12 autorisations Discord d'affilée), il explique "un bot relais = un salon Listener, invitez seulement le nombre de salons voulus" et considère le serveur prêt dès 1 relais. Textes du guide et de l'accueil mis à jour.
- Rôles créés automatiquement à l'arrivée du bot principal (Speaker, Squad Lead, rôle d'accès...) : plus besoin de cliquer "Créer les rôles". Résultat dans le journal (`roles_created_on_join` ou `roles_on_join_failed`) ; le bouton reste pour réparer.
- Le panneau oublie un serveur quand le bot le quitte et ignore un serveur où il n'est plus : fin des "panel_channel_refused" envoyés pour un serveur désinstallé trois jours plus tôt.

## v69

- Relais muets corrigés : depuis disgo v0.20.0, un bot n'émettait et ne captait rien tant que son chiffrement DAVE n'était pas "prêt", ce qui n'arrive jamais en mode non chiffré ni sans clé de chiffrement. Le 13/11 sur server A, 5 relais sur 8 n'ont rien émis pendant toute la session, et la réparation automatique (nouvelle connexion du relais quand le chiffrement échoue) ne se déclenchait plus. La session DAVE répond maintenant toujours "prête" à disgo ; l'audio passe en clair en mode non chiffré et les échecs de chiffrement relancent de nouveau le relais.
- Diagnostic : toutes les minutes de session, nombre de personnes dans le salon de chaque relais et état de son chiffrement (`relay_health`). Un relais qui ne peut pas chiffrer pendant 2 minutes alors que des gens écoutent est signalé dans le rapport ("silent_relay").
- Panneau refusé (droit de créer des salons retiré) : le serveur n'est plus retenté toutes les 15 secondes mais toutes les 5 minutes, ce qui arrête le flot de "panel_channel_refused" et "home_category_refused" dans le journal.

## v68

- Garde-fou de reconnexion (`internal/gatewayguard`) sur le bot principal et les 11 relais : chaque nouvelle session Discord est comptée (`data/gateway-guard.json`, conservé au redémarrage, remis à zéro à chaque nouvelle version). 5 sessions en une heure : message privé au propriétaire de l'application et état des goroutines dans `DIAG_DIR`. 100 en 24 h : la connexion du bot est coupée avant que Discord ne révoque son token (limite vers 1000) ; le site reste en ligne et le bot revient au déploiement suivant.
- Plus aucune boucle directe sur un cache disgo : tout passe par `slices.Collect`, et un test (`internal/cacheiter`) refuse toute nouvelle boucle de ce type (cause de l'interblocage de v66).

## v67

- Bot principal figé depuis v66 : la reprise des membres en vocal au démarrage gardait le verrou du cache vocal pendant qu'elle ajoutait des membres. À la première mise à jour vocale, la lecture du gateway se bloquait : battements de cœur ratés, reconnexion toutes les 3 minutes (environ 480 par jour), boutons du panneau sans réponse. La liste est maintenant copiée avant d'être traitée.

## v66

- disgo v0.19.3 à v0.20.0. Le 10/11, Discord a envoyé un type de salon inconnu (20) dans GUILD_CREATE : disgo v0.19.3 refusait le message, le bot principal se reconnectait en boucle (166 fois en une heure) et Discord a révoqué son token. v0.20.0 accepte les types inconnus.
- Gardes devenues inutiles supprimées (`safeGateway`, `safeUDPConn`) ; événements vocaux par `pool.VoiceEventsOpt` (disgo ne les accepte plus sur une connexion existante) ; membres en vocal au démarrage récupérés par l'API (les événements de guilde ne portent plus la liste des membres).

## v65

- Référencement : titre et description propres à l'accueil et au guide, URL canonique, balises Open Graph et Twitter, JSON-LD (SoftwareApplication) sur l'accueil, `robots.txt` et `sitemap.xml` ; les pages connectées (serveurs, administration) sont en `noindex`.

## v64

- Mise à jour automatique sur la VM : `deploy/ovh/maj-auto.sh` en cron toutes les 30 minutes (compte `voicerelay`), lance `mettre-a-jour.sh` quand `origin/main` a avancé, jamais pendant une session. Installation : `sudo -u voicerelay INSTALLER=true /opt/voice-relay/deploy/ovh/maj-auto.sh`.

## v63

- Fréquentation sur la page d'administration : visiteurs par jour (sans adresse conservée), pages d'accueil et guide vues, clics sur Se connecter, connexions réussies ou refusées, listes de serveurs, installations, désinstallations, dons. Compteurs dans `data/stats.json`, 400 jours gardés.

## v62

- Voice Relay est gratuit : le site ne vend plus d'abonnement. Dons PayPal à la place (`DONATION_AMOUNTS`, défaut 3,5,10,20), ouverts à tout visiteur, sans contrepartie. Les abonnements ne s'affichent que si `SUBSCRIPTION_REQUIRED=true`. `PRICE_LABEL` et `PAYPAL_PLANS` supprimés.
- Accueil refondu en univers animé : carte radar du Speaker vers six salons Listener, retour du Squad Lead, avant/après de la cacophonie, chiffres. Tout s'arrête avec "réduire les animations".
- Guide pas à pas `/guide` (fr, en) avec captures floutées, repères animés et menu d'étapes.
- Rôles : un rôle créé à la même position que celui du bot n'est plus signalé "au-dessus du bot".

## v44 à v61

- Rôle d'accès automatique, catégorie et panneau privés, rapports effacés au bout de 30 minutes, étapes guidées sur le site, langue du serveur, photos de profil des bots.

## v43

- Usage générique, sans référence à un jeu : rôles Speaker et Squad Lead, salons Speaker et Listener 1 à N, catégorie "Voice Relay", factions supprimées (`FACTION_NAME`, `FACTION_ROLE`).
- Toutes les applications servent : le bot principal capte le salon Speaker (rôle Speaker seulement), chaque bot relais diffuse dans un salon Listener.
- Retour des chefs : le Squad Lead de chaque salon Listener est entendu dans le salon Speaker seulement ; coupure par salon ou totale depuis le panneau.
- Variables renommées, anciens noms encore lus : `SPEAKER_ROLE`, `SPEAKER_CHANNEL`, `LISTENER_CHANNEL`, `LISTENER_COUNT`, `CATEGORY`, `CATEGORY_AFTER`.
- Sons de début et de fin retirés (désactivés par défaut), image Caddy figée par empreinte.

## v26 à v42

- Site d'administration, abonnements, paiement PayPal sur le site, panneau permanent, accès restreint ou ouvert, désinstallation, journal des installations, limitation des clics et des requêtes.

## v25

- Import initial du code dans le dépôt privé `TepqeT/voice-relay` : relais du salon principal vers les salons d'écoute, chefs (touche prioritaire ou permanente), salons créés par session, contrôle d'accès, modération, rapports qualité, diagnostics.
