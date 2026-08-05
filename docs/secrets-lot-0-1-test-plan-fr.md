# Secrets — cahier de test des lots 0 et 1

## Préconditions

- utiliser l'image produite depuis `feat/secrets-lot-0-1-file-store` ;
- disposer d'une stack de test `compose/secrets-test` ;
- si possible, connecter également un hôte SSH possédant le même chemin logique ;
- ne jamais utiliser un secret de production pour ces essais.

Exemple minimal :

```yaml
services:
  test:
    image: alpine:3.22
    command: ["sh", "-c", "test \"$$(cat /run/secrets/test_token)\" = first-value && sleep infinity"]
    secrets:
      - test_token

secrets:
  test_token:
    file: ./.secrets/test_token
```

## 1. Création

1. Ouvrir **Settings → Secrets**.
2. Vérifier que l'hôte affiché est le bon.
3. Charger `compose/secrets-test`.
4. Créer `test_token` avec la valeur `first-value`.
5. Vérifier que la valeur n'apparaît ni dans la liste ni dans les logs Dockman.
6. Sur l'hôte, contrôler :

   ```console
   stat -c '%a %U:%G %n' /server/stacks/secrets-test/.secrets /server/stacks/secrets-test/.secrets/test_token
   ```

   Résultat attendu : répertoire `700`, fichier `600`.

## 2. Utilisation sans Dockman

1. Lancer la stack depuis Dockman.
2. Vérifier que le conteneur reste actif.
3. Arrêter Dockman uniquement.
4. Depuis l'hôte, exécuter `docker compose up -d` dans le dossier de test.
5. Résultat attendu : la stack fonctionne sans API, base SQLite ou processus Dockman.

## 3. Remplacement atomique

1. Révéler `test_token`, remplacer sa valeur par `second-value`, puis enregistrer.
2. Vérifier qu'aucun fichier `.dockman-secret-*` ne reste dans `.secrets`.
3. Recréer le service et contrôler `/run/secrets/test_token`.
4. Résultat attendu : seule la valeur complète `second-value` existe ; jamais de fichier partiel.

## 4. Suppression

1. Supprimer `test_token` et saisir `CONFIRM`.
2. Vérifier que seul ce fichier disparaît.
3. Résultat attendu : aucun Compose down, redémarrage ou suppression de volume n'est déclenché.

## 5. Validation et confinement

Essayer successivement les noms `../token`, `folder/token`, `.`, un nom commençant par un espace et un nom de plus de 128 caractères.

Résultat attendu : toutes les opérations sont refusées et aucun fichier n'est créé hors de `.secrets`.

Essayer une valeur supérieure à 1 MiB. Résultat attendu : HTTP 413 et aucun fichier temporaire résiduel.

## 6. Protection Git

1. Recréer `test_token`.
2. Prévisualiser un push avec le profil **all files**.
3. Refaire la prévisualisation avec l'autorisation sensible ponctuelle.
4. Résultat attendu : `.secrets` et `test_token` ne figurent jamais parmi les éléments transférables.

## 7. Isolation multi-hôte

1. Sur `local`, enregistrer `test_token=local-value`.
2. Passer sur l'hôte SSH sans changer `compose/secrets-test`.
3. Enregistrer `test_token=remote-value`.
4. Revenir sur chaque hôte et révéler la valeur.
5. Résultat attendu : chaque hôte conserve sa propre valeur et aucun secret distant n'apparaît sous `/config` local.

## 8. Non-régression et overhead

1. Laisser l'interface inactive pendant cinq minutes.
2. Vérifier l'absence de requêtes périodiques `/secrets`, de nouveau thread et de logs répétitifs.
3. Parcourir Files, Monitor, Updates, Git et Volumes.
4. Résultat attendu : aucune modification de comportement hors du nouvel onglet et aucun coût CPU au repos attribuable aux secrets.
