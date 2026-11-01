# Plan du service hébergé

Document de cadrage, sans code. État de départ : v28 (une instance Docker par faction, configuration par variables compose, bots privés).

Légende : **Fait** = constaté dans le code ou une source citée. **Hypothèse** = à vérifier avant de s'engager.

## 1. Ce qui existe déjà et sert de base

- Fait : le code est déjà multi-serveur. `internal/store` range les liaisons de salons, rôles, langue et Hérauts actifs par ID de serveur (`store.yaml`), et `/setup` les modifie depuis Discord.
- Fait : les commandes sont enregistrées par serveur (`SetGuildCommands`), donc propagées tout de suite.
- Fait : tous les réglages RvR (`RVR_*`, `ACCESS_*`, `SQUAD_*`, `CLASS_*`, `EVENT_LABEL`) est lu une seule fois au démarrage (`internal/config`) et vaut pour tous les serveurs de l'instance.
- Conséquence : le chantier principal est de faire passer ces réglages de "un par instance" à "un par serveur", dans le store.

## 2. Bots publics partagés entre serveurs

Modèle : une seule application "bot principal" et un groupe de Hérauts publics, invités dans tous les serveurs clients.

- Fait (Discord) : un compte bot ne peut être que dans un seul salon vocal par serveur, mais dans un salon de chaque serveur en même temps. Un Héraut sert donc une escouade dans chaque serveur client simultanément.
- Conséquence : le nombre de Hérauts fixe le nombre maximal d'escouades par serveur, pas le nombre de serveurs. 10 Hérauts = 10 escouades par serveur, pour tous les clients.
- Le goulot devient la machine : connexions vocales simultanées, CPU DAVE et Opus, bande passante (voir section 7).
- Deux factions d'un même serveur : aujourd'hui deux instances avec deux jeux de bots. En hébergé, il faut soit deux jeux de bots publics (simple, double le nombre d'applications), soit un même Héraut qui sert la faction A puis B (impossible en simultané dans un même serveur). Recommandation : garder deux jeux, "côté A" et "côté B", et un champ faction dans la configuration du serveur.
- Fait (Discord) : la création d'applications est limitée par compte (déjà rencontré : les Hérauts 6 à 10 ne sont pas créés). Il faut un compte ou une équipe Discord Developer dédiée au service, pas le compte personnel.
- Nom commercial sans marque NCSoft ni "Aion" dans le nom des bots.

## 3. Installation guidée : site web et /setup automatique

Objectif : l'administrateur fait tout depuis un site web avec son compte Discord. Ensuite, il n'a plus qu'à donner les rôles créés à ses joueurs.

### Parcours sur le site

1. Connexion avec Discord (OAuth2, scopes `identify` et `guilds`). Paramètre `state` aléatoire lié à la session, contre le CSRF.
2. Liste des serveurs de l'utilisateur. Fait (Discord) : `GET /users/@me/guilds` renvoie pour chaque serveur les permissions de l'utilisateur. On n'affiche que les serveurs où il a "Gérer le serveur", avec l'état : bot absent, installation incomplète, prêt.
3. Choix du serveur, puis bouton "Inviter le bot principal". Fait (Discord) : le lien d'autorisation accepte `guild_id` et `disable_guild_select=true`, ce qui présélectionne et verrouille le serveur choisi, avec les permissions exactes dans `permissions`.
4. Invitation des Hérauts. Fait (Discord) : chaque bot est une application distincte, et chaque application demande sa propre autorisation par un humain. On ne peut pas inviter 10 Hérauts en un clic. Le site affiche donc une liste à cocher, un bouton par Héraut, qui passe au vert dès que le bot est arrivé (événement `GuildMemberJoin`, déjà géré par `onGuildMemberAdd`).
5. Formulaire de configuration (libellé, factions, nombre d'escouades, accès, classes, sons, langue). Il écrit en base les mêmes réglages que `/setup` (section 4) : le site et Discord sont deux interfaces sur une seule configuration.
6. Bouton "Tout créer", qui lance le `/setup` automatique (ci-dessous), puis écran final : liste des rôles à donner aux joueurs, contrôle des permissions en vert ou rouge.

### /setup automatique

Ce que le bot crée lui-même, sans action manuelle, avec "Gérer les rôles" et "Gérer les salons" :
- Rôles, selon les options choisies : Raid Lead, Chef d'escouade, Board, accès confirmé par faction, rôles de faction si le serveur n'en a pas, un rôle par classe. Couleurs et noms par défaut modifiables.
- Fait (Discord) : un rôle créé par le bot est placé sous le rôle le plus haut du bot, donc le bot peut toujours le gérer ensuite. Les rôles existants du serveur réutilisés (par exemple un rôle de faction déjà présent) doivent être sous le rôle du bot : le contrôle le vérifie et explique comment déplacer le rôle du bot si besoin.
- Salon texte du panneau, avec le panneau publié dedans. Les salons vocaux restent créés au lancement de chaque raid, comme aujourd'hui.
- Enregistrement en base des ID créés : un second `/setup` ou un clic sur "Réparer" recrée ce qui a été supprimé et ne duplique rien.
- `/setup` dans Discord propose le même assistant, pour ceux qui ne passent pas par le site, et un bouton "Ouvrir le site" vers la page de ce serveur.

Ensuite, l'administrateur ne fait plus qu'une chose : donner les rôles à ses joueurs. Les Raid Leads nomment déjà les chefs d'escouade et le Board accorde l'accès depuis Discord.

### Architecture

- Serveur HTTP dans le même binaire Go que le bot (paquet `internal/web`) au début : il lit et écrit la même base et peut demander au bot de lancer l'assistant. Il pourra être séparé plus tard, avec une file de commandes en base, si plusieurs instances de bots tournent.
- Sessions côté serveur, cookie `HttpOnly` `Secure` `SameSite=Lax`. Jeton OAuth de l'utilisateur gardé seulement le temps de lister ses serveurs, jamais le token des bots.
- Droit de modifier un serveur revérifié à chaque requête : utilisateur avec "Gérer le serveur" dans ce serveur, d'après le cache du bot, pas seulement d'après la liste OAuth vue à la connexion.
- Hypothèse : une page simple rendue côté serveur (templates Go, peu de JavaScript) suffit, et évite une seconde pile technique.

Ce qui change dans le code : `internal/web` (OAuth, sessions, pages), `internal/setup` (assistant de création idempotent, partagé entre le site et `/setup`), store avec les ID créés par serveur, configuration du serveur web (`WEB_ADDR`, `WEB_BASE_URL`, secret client OAuth du bot principal en variable d'environnement).

## 4. Configuration par serveur via /setup

Toutes les variables propres à un serveur passent en base. Les variables d'infrastructure restent en environnement.

### Restent en variables d'environnement (infrastructure)

| Variable | Raison |
|---|---|
| `DISCORD_OWNER_BOT_TOKEN`, `DISCORD_SPEAKER_BOT_TOKEN_N` | Secrets du service, jamais en base partagée |
| `STORE_PATH` (devient une URL de base) | Infrastructure |
| `OTEL_ENDPOINT`, `LOG_LEVEL`, `VOICE_LOG_LEVEL` | Exploitation |
| `DIAG_DIR`, `DIAG_INTERVAL`, `REPORTS_DIR` | Exploitation (un sous-dossier par serveur) |
| `SOUNDS_DIR` | Sons fournis par le service |
| `PRIORITY_SPEAKER` | Comportement du service, identique pour tous |

### Passent en base, avec leur commande

| Variable actuelle | Future commande `/setup` |
|---|---|
| `EVENT_LABEL` | `/setup general libelle:<texte>` |
| `REPORT_LANG` | `/setup general langue:<fr/en>` (fusion avec la langue déjà gérée par `/setup`) |
| `REPORT_TO_DISCORD` | `/setup rapports publier:<oui/non>` |
| `SOUND_ON_START`, `SOUND_ON_STOP` | `/setup sons debut:<choix> fin:<choix>` (liste des sons du service) |
| `SESSION_IDLE_TIMEOUT` | `/setup general arret-auto:<minutes>` |
| `FACTION_NAME` | `/setup faction nom:<texte>` |
| `FACTION_ROLE` | `/setup faction role:<@rôle>` |
| `DYNAMIC_CHANNELS` | `/setup salons mode:<auto/existants>` |
| `RVR_CATEGORY` | `/setup salons categorie:<texte>` |
| `RVR_CATEGORY_AFTER` | `/setup salons apres:<catégorie>` |
| `RAID_LEAD_CHANNEL`, `SQUAD_CHANNEL` | `/setup salons raid-lead:<texte> escouade:<texte>` |
| `SQUAD_COUNT` | `/setup salons escouades:<1-10>` (plafonné au nombre de Hérauts) |
| `VOICE_REGION` | `/setup salons region:<choix>` |
| `RAID_LEAD_ROLE` | `/setup roles raid-lead:<@rôle>` (existe déjà comme rôle de capture) |
| `SQUAD_LEAD_ROLE` | `/setup roles chef:<@rôle>` |
| `SQUAD_LEAD_KEY` | `/setup chefs touche:<prioritaire/toujours>` |
| `SQUAD_LEADS_TO_SQUADS` | `/setup chefs entre-escouades:<oui/non>` |
| `ACCESS_ROLE` | `/setup acces role:<@rôle>` |
| `BOARD_ROLE` | `/setup acces board:<@rôle>` |
| `ACCESS_CHANNEL` | `/setup acces salon:<#salon>` (voir section 6 : Message Content) |
| `CLASS_ROLES` | `/setup classes roles:<@rôle ...>` (sélecteur de rôles multiple) |
| `WAITING_CHANNEL` | `/setup classes attente:<texte ou #salon>` |
| `CLASS_RULE` | `/setup classes regle:<texte>`, avec aperçu de la répartition sur 10 joueurs fictifs |

Ce qui change dans le code :
- `ChannelOptions`, `RvROptions`, `ClassSortOptions` ne sont plus des champs du `Service` mais une fonction `optionsFor(guildID)` qui lit le store, avec cache invalidé à chaque `/setup`.
- `store.Store` gagne une structure `GuildSettings` et le YAML est remplacé par une base (SQLite au début, PostgreSQL si plusieurs instances).
- Les commandes clic droit (`<label> <faction> : autoriser`, etc.) dépendent déjà du serveur : elles sont recalculées à chaque `/setup` qui change libellé ou faction.
- Le choix des intents ne peut plus dépendre de la configuration d'un serveur (voir section 6).

## 5. Vérification d'abonnement par ID de serveur

- Table `subscriptions` : ID de serveur, offre, date de fin, nombre d'escouades autorisées, état (essai, actif, suspendu).
- Contrôle au démarrage de raid (`startHostRaid`) et à `/setup` : serveur non abonné = message expliquant comment s'abonner, aucun salon créé.
- Le bot reste dans le serveur à l'expiration (pas de départ automatique), seules les fonctions de raid sont bloquées.
- Paiement : hors Discord (site avec Stripe ou équivalent) qui écrit dans la table ; ou monétisation native Discord (App Subscriptions, SKU par serveur). Hypothèse à vérifier : disponibilité de la monétisation Discord pour une équipe en France et commission appliquée, à lire dans la documentation Discord "Monetization" avant de choisir.
- Ce qui change dans le code : un garde `entitled(guildID)` dans le manager, une commande `/abonnement` qui affiche l'état.

## 6. Intents privilégiés et vérification Discord

- Fait (Discord, support "Bot Verification and Data Whitelisting") : un bot non vérifié ne peut pas rejoindre plus de 100 serveurs. La vérification demande l'identité du propriétaire de l'équipe et une description de l'usage des données.
- Fait (Discord) : au-delà de 100 serveurs, chaque intent privilégié doit être demandé et justifié pendant la vérification.
- Intents utilisés aujourd'hui (`internal/bot/bot.go`) :

| Intent | Privilégié | Usage | Justification ou alternative |
|---|---|---|---|
| `Guilds` | Non | Salons, rôles | - |
| `GuildVoiceStates` | Non | Qui est dans quel salon | - |
| `GuildMembers` | Oui | Rôles des membres (capture, accès, classes) | Justifiable : le relais filtre la voix par rôle. Alternative partielle : les états vocaux portent le membre, mais pas les changements de rôle hors vocal |
| `GuildMessages` + `MessageContent` | Oui (`MessageContent`) | Salon d'accès (`ACCESS_CHANNEL`) | Difficile à justifier à grande échelle. Recommandation : remplacer par une commande `/acces` avec liste de membres ou un formulaire, et supprimer l'intent |

- Les Hérauts n'utilisent que `GuildVoiceStates` (`internal/pool/service.go`) : pas d'intent privilégié, pas de vérification spécifique hormis le seuil de 100 serveurs, qui s'applique à chaque application.
- Fait (Discord) : au-delà de 2 500 serveurs par connexion, le sharding est obligatoire. Hors de portée au début, mais le choix d'une base partagée (section 4) le prépare.

## 7. Charge DAVE par connexion vocale

Faits issus du code et du CHANGELOG :
- Une voix active = environ 50 paquets Opus par seconde (trames de 20 ms).
- Le mélangeur réencode à 64 kbit/s.
- Chaque Héraut assourdi par le serveur ne reçoit plus rien (Assourdir des membres) : il ne fait que chiffrer sa sortie. Sans cette permission il déchiffre puis jette 50 paquets par seconde et par personne qui parle.
- Le bot principal déchiffre chaque Raid Lead et chaque chef d'escouade relayé.

Estimation par raid (hypothèse, ordre de grandeur, à mesurer) :

| Élément | Par raid de 10 escouades, 2 Raid Leads, 10 chefs |
|---|---|
| Connexions vocales | 11 (1 principal + 10 Hérauts) |
| Flux déchiffrés (principal) | 12 au plus, 600 paquets/s |
| Flux chiffrés (Hérauts) | 10, 500 paquets/s |
| Bande passante sortante | 10 x 64 kbit/s, environ 0,7 Mbit/s avec en-têtes |
| Bande passante entrante | 12 x environ 40 à 64 kbit/s |

- Hypothèse : le coût CPU dominant est le décodage et réencodage Opus du mélangeur, pas DAVE (chiffrement AES-GCM par trame, peu coûteux). Les renégociations MLS à chaque arrivée ou départ de membre sont ponctuelles.
- Mesure à faire avant de vendre : un raid réel de 2 h avec `gdc_process_cpu_time_seconds_total` et `gdc_process_memory_usage_bytes` (déjà exposés, `docs/METRICS.md`) et la ligne processeur du rapport qualité. En déduire CPU et mémoire par connexion vocale, puis le nombre de raids simultanés par cœur.
- Risque connu : bug amont "missing key ratchet" (golibdave v0.3.0), contourné par reconnexion. À suivre avant d'ouvrir à plusieurs serveurs : un incident touche tous les clients d'un Héraut.

## 8. Découpage en lots livrables

| Lot | Contenu | Ce qui change dans le code actuel | Prérequis |
|---|---|---|---|
| 0 | Mesures de charge | Aucun code : un raid instrumenté, tableau CPU, mémoire et bande passante par connexion | Raid réel |
| 1 | Configuration par serveur | `GuildSettings` dans `internal/store`, `optionsFor(guildID)` dans le manager, lecture compose conservée comme valeurs par défaut | - |
| 2 | `/setup` complet | Nouvelles sous-commandes et composants dans `internal/bot` (`setup_ui.go`, `handlers_setup.go`), clés i18n dans les 8 locales | Lot 1 |
| 3 | Suppression de `MessageContent` | `/acces` remplace `ACCESS_CHANNEL`, retrait de l'intent dans `bot.go` | - |
| 4 | /setup automatique | `internal/setup` : création idempotente des rôles et du salon du panneau, contrôle des permissions, bouton Réparer ; message d'accueil sur `GuildJoin` | Lots 1, 2 |
| 4b | Site web | `internal/web` : connexion Discord, liste des serveurs gérés, invitations présélectionnées du bot et des Hérauts, formulaire de configuration, bouton "Tout créer" | Lots 4, 5 |
| 5 | Base de données | SQLite puis PostgreSQL derrière `store.Store`, migration du YAML existant | Lot 1 |
| 6 | Abonnements | Table et garde `entitled(guildID)`, commande `/abonnement`, intégration paiement | Lot 5 |
| 7 | Bots publics et vérification | Nouvelles applications au nom commercial, vérification Discord, justification de `GuildMembers` | Lots 3, 4b, 6 |
| 8 | Exploitation | Tableaux de bord par serveur, alertes, sauvegardes de la base, page d'état | Lot 5 |

Ordre recommandé : 0, 1, 3 en parallèle, puis 2, 4, 5, 4b, 6, 7, 8. Les serveurs actuels (Aion 2 France) migrent au lot 1 sans changement visible : la configuration compose devient leur configuration en base.

## Sources

- Discord Developer Portal, documentation "Gateway Intents" et "Sharding" : https://discord.com/developers/docs/events/gateway
- Discord Developer Support, article "Bot Verification and Data Whitelisting" (rechercher le titre sur https://support-dev.discord.com, lien direct non vérifié depuis la session)
- Discord Developer Portal, "OAuth2" (scopes, `guild_id`, `disable_guild_select`) : https://discord.com/developers/docs/topics/oauth2
- Discord Developer Portal, "Monetization" : https://discord.com/developers/docs/monetization/overview
- Code du dépôt : `internal/bot/bot.go` (intents), `internal/pool/service.go`, `internal/store/store.go`, `internal/config/config.go`, `docs/METRICS.md`, `CHANGELOG.md`.
