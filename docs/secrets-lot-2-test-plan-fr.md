# Secrets — cahier de test du lot 2

## Préconditions

- utiliser l'image `integration` publiée pour le lot 2 ;
- conserver une stack de test, sans secret de production ;
- répéter au moins les tests 1 et 5 sur un hôte SSH si disponible.

## 1. Bouton de révélation

1. Ouvrir **Settings → Secrets** et charger la stack.
2. Cliquer sur l'œil de la ligne d'un secret.
3. Vérifier que la boîte s'ouvre avec une valeur masquée, y compris pour une valeur multiligne.
4. Cliquer sur l'œil du champ : la valeur doit devenir lisible immédiatement.
5. Cliquer à nouveau : elle doit être remasquée.
6. Fermer puis rouvrir : elle doit être masquée par défaut.

## 2. Analyse Compose

Utiliser :

```yaml
services:
  app:
    image: alpine:3.22
    secrets:
      - database_password
      - source: shared_token

secrets:
  database_password:
    file: ./.secrets/database_password
  shared_token:
    external: true
```

1. Charger le dossier de la stack.
2. Vérifier que `database_password` est signalé manquant et que `app` est affiché comme consommateur.
3. Créer le secret avec le bouton proposé : son état doit devenir **ready**.
4. Vérifier que `shared_token` est indiqué **external** et qu'aucun fichier local n'est demandé.
5. Déclarer temporairement `file: ../token` : Dockman doit signaler que la source sort de `.secrets`.

### Nom de clé différent du fichier

Utiliser ensuite :

```yaml
secrets:
  database_password:
    file: ./.secrets/db-password.txt
```

Résultat attendu : aucune erreur de correspondance de nom. Dockman propose de
créer `db-password.txt`, puis affiche la référence comme prête.

## 2 bis. Sélecteur de stack

1. Ouvrir l'onglet Secrets sur le host local.
2. Vérifier que le champ propose les stacks, regroupées par alias.
3. Choisir une stack : elle doit être chargée sans recopier son chemin.
4. Passer sur un host SSH : la liste doit être remplacée par celle de ce host.
5. Créer ensuite une stack et utiliser le bouton de rafraîchissement du champ.
6. Vérifier que la saisie manuelle reste possible si une stack dépasse les limites de découverte.

## 3. Historique borné

1. Remplacer cinq fois la valeur de `database_password`.
2. Ouvrir son historique.
3. Vérifier que trois versions seulement sont proposées.
4. Restaurer la plus ancienne version affichée puis révéler la valeur.
5. Résultat attendu : la bonne valeur revient et la valeur remplacée reste elle-même récupérable.

## 4. Suppression et récupération

1. Supprimer un secret avec `CONFIRM`.
2. Vérifier sa présence dans **Recover deleted secrets**.
3. Ouvrir son historique et restaurer une version.
4. Vérifier que le fichier actif revient en mode 0600.

## 5. Multi-hôtes et overhead

1. Créer des historiques différents pour le même chemin logique sur `local` et sur un hôte SSH.
2. Vérifier qu'aucune version ne traverse les hosts.
3. Laisser Dockman inactif et vérifier qu'aucune requête périodique `/secrets` n'apparaît.
4. Vérifier que `.secrets`, y compris `.history`, ne figure jamais dans une prévisualisation Git.
