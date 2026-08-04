# Cahier de test — Updates lot 7 : nettoyage sécurisé des anciennes images

## Principes à contrôler

- Le nettoyage est désactivé par défaut.
- Il ne s'exécute qu'après une mise à jour protégée entièrement réussie.
- Une transaction de stack en échec ou rollback ne déclenche aucune suppression.
- Dockman supprime uniquement l'identifiant exact d'une ancienne image enregistrée pendant l'opération.
- La suppression Docker n'utilise jamais `force`.
- Une image taguée, utilisée par un conteneur actif ou arrêté, ou nécessaire à une image descendante est conservée.
- Les mises à jour protégées d'infrastructure et la mise à jour de Dockman conservent leurs images de secours ; ce nettoyage concerne uniquement les politiques automatiques explicitement configurées.

## 1. Non-régression avec nettoyage désactivé

1. Laisser **Clean previous images safely** désactivé sur une cible.
2. Effectuer une mise à jour automatique réussie.
3. Vérifier que l'ancienne image reste présente.
4. Vérifier qu'aucune ligne n'est créée dans **Previous image cleanup**.

## 2. Conservation recommandée d'une image

1. Activer le nettoyage et conserver `1` image précédente.
2. Effectuer une première mise à jour réussie.
3. Vérifier que l'ancienne image est affichée comme `pending`/conservée et reste disponible pour un rollback manuel.
4. Publier ou cibler une nouvelle version, puis effectuer une seconde mise à jour réussie.
5. Vérifier que l'image précédente la plus récente est conservée et que la plus ancienne est supprimée si elle n'est plus référencée.

## 3. Suppression immédiate avec conservation à zéro

1. Configurer la conservation à `0` sur un conteneur de test.
2. Effectuer une mise à jour réussie avec contrôle de santé.
3. Vérifier que l'ancien identifiant d'image passe à l'état `removed`.
4. Vérifier que l'image courante n'est jamais supprimée.

## 4. Protection des conteneurs arrêtés

1. Créer un second conteneur arrêté qui utilise l'image destinée à devenir ancienne.
2. Mettre à jour la cible avec conservation à `0`.
3. Vérifier que l'image reste `pending` avec une raison indiquant qu'elle est encore référencée.
4. Supprimer le conteneur de test arrêté.
5. Cliquer sur **Retry retained**.
6. Vérifier que l'image devient `removed`.

## 5. Protection des tags

1. Ajouter volontairement un second tag à l'ancienne image avant la mise à jour.
2. Effectuer la mise à jour avec conservation à `0`.
3. Vérifier que l'image reste conservée avec le tag concerné dans la raison.
4. Retirer ce tag, puis utiliser **Retry retained**.
5. Vérifier la suppression sûre.

## 6. Transaction de stack et rollback

1. Activer le nettoyage sur une stack multi-conteneurs avec conservation à `0`.
2. Provoquer l'échec de santé du dernier service après qu'un premier service a été remplacé.
3. Vérifier le rollback complet de la stack.
4. Vérifier qu'aucune ancienne image de cette transaction n'est enregistrée pour suppression.
5. Corriger la stack et relancer avec succès.
6. Vérifier que le nettoyage ne commence qu'après la réussite de l'ensemble de la transaction.

## 7. Configuration par labels

Tester l'équivalent Compose :

```yaml
labels:
  dockman.update: "true"
  dockman.update.cleanup: "true"
  dockman.update.cleanup.keep: "1"
```

Vérifier qu'une valeur de conservation hors plage ou invalide désactive seulement le nettoyage sûr, sans empêcher le scan ni la mise à jour protégée.

## 8. Persistance et charge de fond

1. Redémarrer Dockman avec une image conservée en attente.
2. Vérifier que la ligne et sa raison sont toujours présentes.
3. Vérifier qu'aucune suppression ne démarre au simple chargement de la page.
4. Observer Dockman au repos : le lot n'ajoute aucun polling ni tâche périodique.
5. Vérifier que l'historique des suppressions terminées reste borné.

## Validation

Le lot est validé si aucune image utilisée ou taguée ne disparaît, si les rollbacks conservent toutes leurs images, si la rétention fonctionne sur plusieurs mises à jour et si les éléments conservés peuvent être retraités explicitement sans `force`.
