# Journal des versions

La version courante est dans `internal/version/version.go` (`Fork`). Une entrée par livraison.

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
