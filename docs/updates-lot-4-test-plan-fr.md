# Lot 4 — cahier de test des mises à jour automatiques protégées

## Préparation

1. Utiliser deux conteneurs de test avec une image publique taguée et reproductible.
2. Dans **Updates**, activer une politique sur chaque conteneur avec un cron temporaire d’au moins 15 minutes.
3. Garder le rollback activé sur le premier et le désactiver sur le second.
4. Vérifier que le bouton **Scan enrolled** ne recrée aucun conteneur : son ID doit rester identique.

## 1. Mise à jour automatique nominale

1. Publier ou sélectionner un tag dont le digest distant est plus récent que celui utilisé.
2. Attendre le créneau planifié.
3. Vérifier dans **Automatic update history** : un run, un résultat `updated`, aucun blocage.
4. Vérifier que le conteneur fonctionne, que son ID a changé et que ses volumes, réseaux, ports, labels et variables sont conservés.
5. Pour un conteneur initialement arrêté, vérifier qu’il reste arrêté après remplacement.

## 2. Isolation entre conteneurs

1. Préparer dans le même créneau une image valide et une image volontairement défaillante.
2. Vérifier que la cible valide est mise à jour même si l’autre échoue.
3. Vérifier que les deux résultats apparaissent séparément dans le même run.

## 3. Rollback et coupe-circuit

1. Sur la cible protégée, utiliser une nouvelle image qui ne passe pas son healthcheck configuré.
2. Vérifier que l’ancien conteneur est restauré et fonctionne.
3. Vérifier le résultat `rolled_back`, les logs consultables et le badge `retry blocked`.
4. Attendre le cron suivant : le même digest ne doit pas être retenté.
5. Cliquer **Retry**, puis attendre le cron suivant : une seule nouvelle tentative est autorisée.
6. Sans cliquer Retry, publier un digest différent : ce nouveau digest doit être automatiquement éligible.

## 4. Protections

1. Vérifier que Dockman lui-même n’est jamais enrôlable par ce mécanisme.
2. Ajouter `dockman.update.disable=true` après un scan mais avant le créneau : l’exécution doit être refusée.
3. Vérifier qu’une image locale, une référence sans tag ou une image épinglée par digest n’est jamais exécutée.
4. Lancer une autre action sur la même stack pendant le créneau : la mise à jour doit échouer proprement sans action concurrente.

## 5. Notifications SMTP

1. Activer les notifications de succès et d’erreur.
2. Vérifier qu’un run automatique envoie un seul résumé groupé avec les résultats.
3. Vérifier qu’un scan manuel n’envoie aucun message.
4. Vérifier qu’un digest bloqué ne génère pas le même message à chaque cron.

## 6. Persistance et redémarrage

1. Provoquer un rollback, puis redémarrer Dockman.
2. Vérifier que l’historique, les logs bornés et le blocage sont toujours visibles.
3. Vérifier que le même digest n’est pas retenté après le redémarrage.

## Critères de validation

- aucune mise à jour pendant un scan manuel ;
- aucune mise à jour hors des cibles explicitement enrôlées ;
- conservation de la configuration Docker et de l’état arrêté/démarré ;
- rollback fiable, isolation des échecs et absence de boucle ;
- historique lisible, acquittement volontaire et notifications cohérentes ;
- aucune nouvelle boucle de fond : hors créneau, l’overhead CPU reste inchangé.
