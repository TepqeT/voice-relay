# Voice Relay

Relais vocal Discord pour les raids : la voix des Raid Leads est diffusée en direct dans chaque salon d'escouade par des bots "Hérauts", les chefs d'escouade peuvent appeler les Raid Leads, et les salons du raid sont créés au lancement puis supprimés à l'arrêt.

Code privé de TepqeT. Ne pas diffuser.

## Fonctions

- Un bot principal écoute le salon Raid Lead ; un Héraut par escouade y rediffuse les calls (topologie en étoile, ou mix-minus quand les chefs d'escouade s'entendent entre eux).
- Plusieurs Raid Leads qui parlent en même temps sont mélangés.
- Chefs d'escouade relayés vers le salon Raid Lead (touche "Appuyer pour parler (prioritaire)" ou en permanence).
- Salons créés par raid et par faction, contrôle d'accès par rôle géré par le Board, modération par les chefs d'escouade.
- Chiffrement de bout en bout Discord (DAVE), rapports qualité et diagnostics détaillés par raid.
- Interface en 8 langues.

## Démarrer

- Déploiement, réglages et procédures : [deploy/LISEZMOI.md](deploy/LISEZMOI.md).
- Tests : `docker build --target test .`
- Image : `docker build -t voice-relay .`

Variables essentielles (le reste est dans `deploy/LISEZMOI.md`) :

| Variable | Obligatoire | Rôle |
|---|---|---|
| `DISCORD_OWNER_BOT_TOKEN` | Oui | Token du bot principal |
| `DISCORD_SPEAKER_BOT_TOKEN_N` | Oui, au moins un | Tokens des Hérauts (`_1`, `_2`...) |
| `STORE_PATH` | Non | Fichier de persistance (défaut `store.yaml`) |
| `SESSION_IDLE_TIMEOUT` | Non | Arrêt automatique si tous les salons sont vides (défaut `10m`, `0` désactive) |
| `OTEL_ENDPOINT` | Non | Export OpenTelemetry (traces, métriques, logs) |
| `LOG_LEVEL` | Non | `debug`, `info`, `warn`, `error` |

Les tokens ne sont jamais committés : ils vivent dans `.env` et `.env.elyos` sur la machine de déploiement.

## Applications Discord

1. Une application par bot (principal, puis un par Héraut) sur https://discord.com/developers/applications.
2. Bot principal : activer "Server Members Intent" (et "Message Content Intent" seulement si `ACCESS_CHANNEL` est utilisé).
3. Inviter le bot principal avec le lien affiché dans les logs au démarrage (`owner bot invite URL`), ou :
   `https://discord.com/oauth2/authorize?client_id=<client_id>&scope=bot&permissions=391565771282752`
   Puis inviter les Hérauts depuis `/setup`.
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
