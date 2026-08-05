# Lot 9 — cahier de test des canaux de notification

## Objectif

Valider les canaux webhook, Gotify, ntfy, Discord et Apprise sans régression SMTP ni impact sur l'exécution des mises à jour.

## 1. Migration et affichage

1. Mettre à jour l'image Dockman sur la branche de test.
2. Ouvrir **Updates**.
3. Vérifier que le bouton **Channels** est présent à côté de **SMTP**.
4. Ouvrir la fenêtre : elle doit fonctionner même sans canal configuré.
5. Vérifier que l'ancienne configuration SMTP et son historique sont toujours présents.

Résultat attendu : aucune erreur de migration, aucune perte SMTP.

## 2. Gotify — test principal

1. Créer une application dans Gotify et copier son application token.
2. Ajouter un canal **Gotify** dans Dockman.
3. Saisir un nom, l'URL de base du serveur sans `/message`, puis le token.
4. Laisser la priorité à `0` pour le choix automatique.
5. En HTTPS public, laisser l'accès privé/HTTP désactivé. Pour une adresse LAN ou du HTTP, activer explicitement cette option.
6. Enregistrer puis cliquer sur l'icône **Send test**.

Résultat attendu : notification reçue, canal affiché comme actif, aucune URL complète ni token réaffiché par Dockman.

## 3. Persistance et rotation du secret

1. Fermer puis rouvrir la fenêtre.
2. Éditer le canal sans ressaisir le token et modifier uniquement sa priorité.
3. Enregistrer et renvoyer un test.
4. Modifier ensuite le token avec un nouveau token valide.
5. Redémarrer complètement Dockman puis renvoyer un test.

Résultat attendu : le champ secret vide conserve le secret existant, la rotation fonctionne et la configuration survit au redémarrage.

## 4. Routage des événements

1. Activer les succès et désactiver les erreurs ; provoquer une mise à jour automatique réussie.
2. Vérifier la notification.
3. Désactiver les succès et activer les erreurs ; provoquer un échec avec rollback.
4. Vérifier la notification d'erreur et l'absence de notification de succès.

Résultat attendu : chaque catégorie suit exactement ses interrupteurs.

## 5. Isolation des canaux

1. Garder le canal Gotify valide.
2. Ajouter un second webhook volontairement invalide ou indisponible.
3. Déclencher un événement planifié.

Résultat attendu : Gotify reçoit le message ; l'autre canal passe en échec dans l'historique ; le scan ou la mise à jour Dockman n'est pas annulé.

## 6. Déduplication

1. Lancer deux scans planifiés produisant exactement le même résultat.
2. Vérifier qu'un seul message est reçu sur un canal qui a réussi.
3. Laisser un second canal échouer au premier scan, le réparer puis relancer le même résultat.

Résultat attendu : le canal déjà livré n'est pas dupliqué et le canal précédemment en échec peut être retenté.

## 7. Sécurité réseau

1. Essayer d'enregistrer une URL HTTP sans activer l'option privée/HTTP.
2. Essayer `https://localhost`, une adresse loopback ou link-local.
3. Essayer une URL contenant `utilisateur:motdepasse@hôte`.
4. Tester une URL qui répond par redirection.

Résultat attendu : les trois premières configurations sont refusées ; une redirection n'est pas suivie et apparaît comme un échec borné.

## 8. Suppression

1. Supprimer un canal.
2. Vérifier que le bouton reste désactivé tant que `CONFIRM` n'est pas saisi exactement.
3. Confirmer puis redémarrer Dockman.

Résultat attendu : le canal et ses credentials chiffrés ne reviennent pas ; l'historique d'exploitation peut rester visible jusqu'à sa rétention normale.

## Limite volontaire de ce lot

Ces canaux notifient les scans et exécutions de mises à jour d'images. Les événements Git (synchronisation, conflit, déploiement et rollback Git) ne sont pas encore routés vers eux. Les webhooks Git entrants constituent également un lot séparé.
