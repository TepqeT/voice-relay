# Voice Relay

Relais vocal Discord : la voix des Speakers est diffusée en direct dans chaque salon Listener par des bots relais, le Squad Lead de chaque salon Listener est entendu dans le salon Speaker, et les salons sont créés au lancement d'une session puis supprimés à l'arrêt.

Code privé de TepqeT. Ne pas diffuser.

## Fonctions

- Le bot principal écoute le salon Speaker (rôle Speaker seulement) ; un bot relais par salon Listener y diffuse leur voix (topologie en étoile).
- Plusieurs Speakers qui parlent en même temps sont mélangés.
- Squad Leads relayés vers le salon Speaker seulement (en permanence ou avec la touche "Appuyer pour parler (prioritaire)") ; les Speakers coupent ce retour par salon ou en entier depuis le panneau.
- Catégorie permanente avec panneau de commande, accès restreint ou ouvert, modération par les Squad Leads.
- Chiffrement de bout en bout Discord (DAVE), rapports qualité et diagnostics détaillés par session.
- Interface en 8 langues.

## Démarrer

- Déploiement, réglages et procédures : [deploy/ovh/LISEZMOI.md](deploy/ovh/LISEZMOI.md).
- Tests : `docker build --target test .`
- Image : `docker build -t voice-relay .`

Variables essentielles (le reste est dans `deploy/ovh/docker-compose.yml`) :

| Variable | Obligatoire | Rôle |
|---|---|---|
| `DISCORD_OWNER_BOT_TOKEN` | Oui | Token du bot principal |
| `DISCORD_SPEAKER_BOT_TOKEN_N` | Oui, au moins un | Tokens des bots relais (`_1`, `_2`...) |
| `STORE_PATH` | Non | Fichier de persistance (défaut `store.yaml`) |
| `SESSION_IDLE_TIMEOUT` | Non | Arrêt automatique si tous les salons sont vides (défaut `10m`, `0` désactive) |
| `OTEL_ENDPOINT` | Non | Export OpenTelemetry (traces, métriques, logs) |
| `LOG_LEVEL` | Non | `debug`, `info`, `warn`, `error` |

Les tokens ne sont jamais committés : ils vivent dans `.env` sur la machine de déploiement.

## Applications Discord

1. Une application par bot (principal, puis un par bot relais) sur https://discord.com/developers/applications.
2. Bot principal : activer "Server Members Intent" (et "Message Content Intent" seulement si `ACCESS_CHANNEL` est utilisé).
3. Inviter le bot principal avec le lien affiché dans les logs au démarrage (`owner bot invite URL`), ou :
   `https://discord.com/oauth2/authorize?client_id=<client_id>&scope=bot&permissions=391565771282752`
   Puis inviter les bots relais depuis le site ou `/setup`.
4. Donner "Assourdir des membres" au bot principal : c'est la plus grosse économie de CPU.

## Documentation technique

- [docs/VOICE_FLOW.md](docs/VOICE_FLOW.md) : circulation du son et composants.
- [docs/LATENCY.md](docs/LATENCY.md) : budget de latence.
- [docs/METRICS.md](docs/METRICS.md) : métriques exposées.
- [docs/INTEGRATION_TESTS_SETUP.md](docs/INTEGRATION_TESTS_SETUP.md) : tests de bout en bout.

## Pile technique

Go 1.27, disgo, DAVE (libdave), libopus, OpenTelemetry.

## Licence

Voir `LICENSE` et `NOTICE`.
