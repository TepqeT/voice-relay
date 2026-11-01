# Relais vocal RvR Aion 2

Voice Relay : relais des Raid Leads RvR vers les escouades, appels des Chefs d'escouade vers les Raid Leads, salons créés au lancement et supprimés à l'arrêt, voix prioritaire, rapport qualité et diagnostic de fin de raid.

## Lancer

Le plus simple : `powershell -ExecutionPolicy Bypass -File .\Demarrer-Relais-RvR.ps1`. Une première fois tel quel pour les vérifications, puis avec `$Apply = $true` en haut du script : tests, démarrage et enregistrement des logs dans `logs\`.

À la main :

```powershell
cd $HOME\source\aion-relay
docker compose up -d --build --remove-orphans
docker compose logs -f asmos
```

Avec les Élyséens (une fois `.env.elyos` rempli) : `docker compose --profile elyos up -d --build --remove-orphans`.

La première construction prend quelques minutes (compilation Go + libdave). Les suivantes réutilisent le cache.

## Panneau de commande

1. Dans le salon texte des Raid Leads, tapez une seule fois `/panneau` : le bot publie un message avec 3 boutons.
2. Épinglez ce message. Les boutons restent valables même après un redémarrage du bot.
3. Boutons :
   - **Lancer le raid** : les bots rejoignent les salons. Les Raid Leads RvR sont entendus dans toutes les escouades, les Chefs d'escouade sont entendus par les Raid Leads RvR.
   - **Arrêter le raid** : son de fin, les bots quittent les salons, le rapport qualité est publié.
   - **État** : configuration et état du relais.
4. Seuls les membres avec le rôle de gestion peuvent utiliser les boutons.
5. Un panneau par faction : `/panneau` du bot Voix de Pandaemonium pour les Asmodiens, du bot Voix de Sanctum pour les Élyséens.
6. Les escouades n'ont besoin que du rôle de faction pour voir et rejoindre leurs salons.

## Bots

| Faction | Écoute le salon Raid Lead | Escouades | Fichier des tokens |
|---|---|---|---|
| Asmodiens | Voix de Pandaemonium | Héraut d'Asmodae 1 à 5 | `.env` |
| Élyséens | Voix de Sanctum | Héraut d'Elysea 1 à 5 | `.env.elyos` |

- Les Hérauts sont attribués aux escouades dans l'ordre de leur numéro (Héraut 1 : Escouade 1, etc.).
- Pour passer à plus d'escouades : créer les Hérauts suivants sur le portail, ajouter leurs tokens (`DISCORD_SPEAKER_BOT_TOKEN_6`, ...) puis augmenter `SQUAD_COUNT`.

## Salons par faction

Au lancement, chaque faction a sa catégorie, supprimée à l'arrêt (les joueurs encore dedans sont déconnectés) :

| Salon | Qui le voit | Qui peut y entrer |
|---|---|---|
| Catégorie "RvR Asmodiens" | Rôle Asmodiens | - |
| Raid Lead | Rôle Asmodiens | Rôle Raid Lead (rôle de capture du `/setup`) |
| Escouade 1 à 5 | Rôle Asmodiens | Rôle Asmodiens |

Pareil côté Élyséens avec le rôle Élyséens et la catégorie "RvR Élyséens". Les Asmodiens ne voient jamais les salons Élyséens, et inversement.

- Nombre d'escouades : `SQUAD_COUNT` (5), plafonné au nombre de Hérauts de la faction.
- Le rôle Chef d'escouade reçoit automatiquement "Voix prioritaire" dans les salons d'escouade.
- Si le bot redémarre pendant un raid, les salons restent : le lancement suivant les réutilise, le bouton Arrêter les supprime.
- Les anciens salons fixes ne servent plus, ils peuvent être supprimés.

Mise en place (une fois par faction) :
1. Créer les rôles **Asmodiens** et **Élyséens** (noms exacts, ou changer `FACTION_ROLE` dans `docker-compose.yml`) et les donner aux joueurs.
2. Paramètres du serveur > Rôles > rôle du bot owner (Voix de Pandaemonium, Voix de Sanctum) : activer **Gérer les salons** et **Gérer les rôles**.
3. Élyséens : copier `.env.elyos.example` en `.env.elyos`, y coller les tokens, lancer avec le profil elyos, puis `/setup` avec le bot Voix de Sanctum (rôles, invitation des speakers).

## Chefs d'escouade (RL de légion vers Raid Lead RvR)

Qui entend qui :

| Qui parle | Où | Qui l'entend |
|---|---|---|
| Raid Lead RvR (rôle Raid Lead) | Salon Raid Lead | Toutes les escouades |
| Chef d'escouade, touche prioritaire | Son salon d'escouade | Son escouade et le salon Raid Lead |
| Chef d'escouade, touche normale | Son salon d'escouade | Son escouade uniquement |
| Membre d'escouade | Son salon d'escouade | Son escouade uniquement |

Mise en place (une fois) :
1. Créer le rôle Discord **Chef d'escouade** (nom exact) et le donner au RL de chaque légion. Un seul rôle suffit pour les deux factions.
2. La permission **Voix prioritaire** est donnée automatiquement à ce rôle dans les salons d'escouade créés.
3. Chaque Chef d'escouade : Paramètres Discord > Voix et vidéo > Mode de saisie : **Appuyer pour parler** (obligatoire, la touche prioritaire ne marche pas en détection de la voix). Puis Raccourcis clavier : garder la touche habituelle pour l'escouade et ajouter **Appuyer pour parler (prioritaire)** sur une autre touche pour le Raid Lead.
4. Relancer le panneau (`/panneau`) pour afficher la consigne aux joueurs.

Si la touche prioritaire ne fonctionne pas en test, passer `SQUAD_LEAD_KEY: always` dans `docker-compose.yml` : les Chefs d'escouade sont alors relayés dès qu'ils parlent (ils doivent utiliser l'appuyer pour parler).

## Tri automatique par classe

Les joueurs prennent **un seul** rôle de classe et rejoignent le salon d'attente. Pendant un raid, le bot les déplace dans les escouades selon la règle des Raid Leads.

- Déclenchement : à l'arrivée d'un joueur dans le salon d'attente (regroupé sur 2 s), ou bouton **Répartir** du panneau (Raid Leads uniquement).
- Restent en attente, et apparaissent dans le récapitulatif du bouton Répartir : joueur sans rôle de classe, joueur avec plusieurs rôles de classe, classe sans place libre, Chef d'escouade (jamais déplacé).
- Avec `DYNAMIC_CHANNELS`, le salon d'attente est créé avec les autres, visible par le même rôle que les escouades (`ACCESS_ROLE` ou rôle de faction). Sinon, il doit exister (nom ou ID).

Mise en place :
1. Créer un rôle par classe et les donner aux joueurs (un seul chacun).
2. Vérifier que le bot principal a **Déplacer des membres** (déjà fait en v20). Sans elle, le récapitulatif l'indique et rien ne plante.
3. Décommenter `CLASS_ROLES`, `WAITING_CHANNEL` et `CLASS_RULE` dans `docker-compose.yml`, relancer, puis republier le panneau (`/panneau`) pour voir le bouton.

Règles (`CLASS_RULE`) :

| Valeur | Effet |
|---|---|
| `balanced` | Chaque classe répartie le plus équitablement possible entre les escouades |
| `balanced; max=6` | Idem, 6 membres au plus par escouade |
| `Clerc=2; Templier=1; *=1` | Quotas par escouade ; `*` pour les autres classes (sans `*`, elles ne sont pas placées) |
| `Clerc=2/1` | 2 Clercs dans l'escouade 1, puis 1 dans chacune des suivantes |

## Commandes Discord (alternative aux boutons)

| Commande | Qui | Effet |
|---|---|---|
| `/panneau` | Raid Lead | Publie le panneau de commande |
| `/start` | Raid Lead | Comme le bouton Lancer (laisser le mode par défaut) |
| `/stop` | Raid Lead | Comme le bouton Arrêter |
| `/status` | Tous | État du relais |
| `/setup` | Admin | Rôles, salons, speakers |

## Sons de début et de fin

- `sounds\debut.ogg` est joué au lancement, `sounds\fin.ogg` à l'arrêt, dans toutes les escouades.
- Version voix française : `powershell -ExecutionPolicy Bypass -File .\Generer-Annonces-Vocales.ps1` (phrases modifiables en haut du script), puis `docker compose restart`.
- Format : Ogg Opus, trames de 20 ms, 15 s maximum. Conversion d'un enregistrement personnel :

```powershell
cd $HOME\source\aion-relay\sounds
docker run --rm -v "${PWD}:/config" linuxserver/ffmpeg -i /config/mon-enregistrement.mp3 -ac 2 -c:a libopus -b:a 96k -frame_duration 20 /config/debut.ogg
cd ..
docker compose restart
```

## Plusieurs Raid Leads

- Les Raid Leads peuvent être plusieurs dans le salon Raid Lead, c'est le fonctionnement normal.
- Quand un seul parle, sa voix est transmise telle quelle, sans perte de qualité.
- Quand plusieurs parlent en même temps, leurs voix sont mélangées pour que les escouades les entendent toutes, au prix d'environ 20 ms de délai en plus.
- Le rapport de fin de raid indique le temps de voix superposées : s'il est élevé, les Raid Leads se coupent souvent la parole.

## Rapport qualité

- Publié à la fin de chaque raid dans le salon où le raid a été lancé, et enregistré dans `data\reports\` (Asmodiens) ou `data-elyos\reports\` (Élyséens).
- Verdict OK / à surveiller / problème, sur : voix captée, pertes, latence du relais, déchiffrement DAVE, reconnexions, erreurs dans les logs.
- Logs utiles après un raid :

```powershell
docker compose logs --since 3h asmos | Select-String -Pattern "rvr quality report|WARN|ERROR"
```

## Diagnostic détaillé (après chaque test)

- Un fichier par raid dans `data\diagnostics\` (Élyséens : `data-elyos\diagnostics\`) (`rvr-<date>-<serveur>.jsonl`), une mesure toutes les 10 s.
- Contenu : pour chaque Raid Lead, paquets perdus, paquets dans le désordre, irrégularité d'arrivée, rafales ; pour chaque salon, micro-coupures, retard en file, paquets jetés, serveur vocal Discord ; temps de mélange (plusieurs Raid Leads présents) et temps de voix réellement superposées ; annonces ; reconnexions ; avertissements ; processeur et mémoire.
- Dans les logs, une ligne `rvr health` toutes les 30 s pendant le raid.
- Le rapport de fin de raid résume ces mesures et donne le nom du fichier.
- Pour l'analyse : laisser les fichiers en place après le test, pour les relire ensuite.

## Réglages (`docker-compose.yml`)

| Variable | Défaut | Rôle |
|---|---|---|
| `PRIORITY_SPEAKER` | `true` | Bots en voix prioritaire |
| `SOUND_ON_START` / `SOUND_ON_STOP` | `debut` / `fin` | Sons automatiques, `none` pour désactiver |
| `REPORT_TO_DISCORD` | `true` | Publier le rapport dans Discord |
| `REPORT_LANG` | `fr` | Langue du rapport (`fr` ou `en`) |
| `SESSION_IDLE_TIMEOUT` | `30m` | Arrêt auto si tous les salons sont vides |
| `DIAG_DIR` | `/data/diagnostics` | Fichiers de diagnostic, `none` pour désactiver |
| `DIAG_INTERVAL` | `10s` | Fréquence des mesures du diagnostic |
| `SQUAD_LEAD_ROLE` | `Chef d'escouade` | Rôle des RL de légion, `none` pour désactiver |
| `SQUAD_LEAD_KEY` | `priority` | `priority` (touche prioritaire) ou `always` |
| `DYNAMIC_CHANNELS` | `true` | Créer les salons au lancement, les supprimer à l'arrêt |
| `FACTION_ROLE` | `Asmodiens` / `Élyséens` | Rôle qui voit les salons de la faction |
| `SQUAD_COUNT` | `5` | Nombre de salons d'escouade |
| `RAID_LEAD_CHANNEL` / `SQUAD_CHANNEL` | `Raid Lead` / `Escouade` | Noms des salons |
| `VOICE_REGION` | vide | Région vocale commune à tous les salons, vide = automatique |
| `EVENT_LABEL` | `RvR` | Nom de l'événement dans la catégorie, les commandes clic droit et les messages |
| `CLASS_ROLES` | vide | Rôles de classe (noms ou ID, séparés par des virgules), vide = tri désactivé |
| `WAITING_CHANNEL` | vide | Salon d'attente du tri par classe, vide = tri désactivé |
| `CLASS_RULE` | `balanced` | Règle de composition des escouades (voir Tri automatique par classe) |

## Voix prioritaire

- Les bots reçoivent "Voix prioritaire" dans chaque salon créé.
- L'atténuation est appliquée par l'application Discord de chaque joueur : à valider en test réel.

## Mettre à jour

Après réception d'une nouvelle version du code dans `voice-relay\` : `docker compose up -d --build --remove-orphans` (ajouter `--profile elyos` si les Élyséens tournent).
