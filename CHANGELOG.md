# Journal des versions

La version courante est dans `internal/version/version.go` (`Fork`). Une entrée par livraison.

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
