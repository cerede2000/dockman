# Cahier de test — Git sync, lot 2

## Périmètre livré

Ce lot ajoute un catalogue multi-dépôts et des opérations Git manuelles :

- import d’un dépôt GitHub public ou privé ;
- création facultative d’un dépôt GitHub personnel ;
- clone isolé dans `/config/git/repositories/<UUID>` ;
- état `up-to-date`, `ahead`, `behind`, `diverged` et `dirty` ;
- fetch, pull fast-forward et push manuels ;
- historique des opérations et suppression du clone local.

Il ne lie encore aucun dépôt à `/compose`, ne modifie aucune stack, et ne déclenche aucun déploiement. L’image de test prévue est `ghcr.io/cerede2000/dockman:git-sync-lot-2`.

## Préparation GitHub

Créer un dépôt jetable initialisé avec un `README.md`, par exemple `dockman-git-sync-test`, avec une branche `main`. Ne pas utiliser un dépôt de production.

Pour HTTPS, utiliser un fine-grained PAT limité au dépôt de test avec **Contents: Read and write**. Si la fonction **Create a new GitHub repository** doit aussi être testée, le token doit disposer de **Administration: Write** conformément à la documentation GitHub de l’endpoint de création. Un PAT classique nécessite `repo` pour un dépôt privé, ou `public_repo` pour un dépôt public.

Réutiliser la clé maître et le volume `/config` validés aux lots 0–1 :

```yaml
services:
  dockman:
    image: ghcr.io/cerede2000/dockman:git-sync-lot-2
    environment:
      DOCKMAN_GIT_SYNC: "true"
      DOCKMAN_GIT_MASTER_KEY_FILE: /run/secrets/dockman_git_key
    secrets:
      - dockman_git_key

secrets:
  dockman_git_key:
    file: ./dockman-git-master.key
```

## Parcours principal

### 1. Import HTTPS privé

1. Dans **Settings → Git**, conserver ou créer l’identifiant HTTPS testé au lot précédent.
2. Cliquer sur **Add repository** puis **Import an existing repository**.
3. Saisir un nom local, l’URL `https://github.com/<compte>/dockman-git-sync-test.git`, la branche `main` et l’identifiant HTTPS.
4. Valider.

Résultat attendu : l’import se termine, le dépôt apparaît `up-to-date`, propre, avec `↑0 ↓0`. Aucune stack existante n’est modifiée.

### 2. Détection et pull d’un changement distant

1. Modifier le `README.md` directement sur GitHub et valider le commit.
2. Dans Dockman, cliquer sur **Fetch remote state**.
3. Vérifier l’état `behind` et `↓1`.
4. Cliquer sur **Pull fast-forward changes**.

Résultat attendu : retour à `up-to-date`. L’historique contient les opérations `fetch` puis `pull` réussies.

### 3. Dépôt public

1. Importer un second dépôt public avec **None (public repository)**.
2. Lancer fetch puis pull.
3. Redémarrer Dockman.

Résultat attendu : les deux dépôts persistent et restent utilisables indépendamment.

### 4. Dépôt SSH privé

1. Importer le dépôt de test avec `git@github.com:<compte>/dockman-git-sync-test.git` et l’identifiant SSH validé au lot précédent.
2. Exécuter fetch puis consulter l’historique.

Résultat attendu : accès réussi avec vérification de la clé d’hôte GitHub ; aucun contournement SSH n’est nécessaire.

### 5. Création GitHub facultative

1. Choisir **Create a new GitHub repository**.
2. Saisir un nom encore libre, choisir privé/public et l’identifiant HTTPS disposant de **Administration: Write**.
3. Valider et vérifier le nouveau dépôt sur GitHub.

Résultat attendu : GitHub crée et initialise le dépôt, puis Dockman le clone. Un refus GitHub est remonté proprement si le nom existe ou si la permission manque.

### 6. Push manuel sans changement

Cliquer sur **Push local commits** sur un dépôt `up-to-date`.

Résultat attendu : opération sans effet, aucun commit artificiel et aucun changement de stack. La création de commits depuis les stacks sera testable avec le lot de liaison suivant.

### 7. Historique et suppression locale

1. Ouvrir **Operation history** et vérifier les dates, états et erreurs éventuelles.
2. Cliquer sur la corbeille d’un dépôt, annuler une fois, puis confirmer.
3. Vérifier GitHub.

Résultat attendu : seule la copie locale gérée par Dockman et son historique local sont supprimés. Le dépôt GitHub existe toujours et les autres dépôts Dockman sont intacts.

## Garde-fous et erreurs

### 8. Entrées refusées

Essayer successivement : une URL avec token intégré, un autre domaine, un port non standard, une query string, un chemin GitHub à trois niveaux, une branche contenant `..`, un doublon de nom et une URL SSH avec un identifiant HTTPS.

Résultat attendu : chaque entrée est refusée avant toute utilisation dangereuse. Aucun secret n’apparaît dans l’URL, les réponses réseau, les logs ou l’historique.

### 9. Mauvais token ou mauvaise branche

Importer avec un token sans accès, puis avec une branche inexistante.

Résultat attendu : erreur explicite, dépôt marqué en erreur si son enregistrement a été créé, aucun répertoire temporaire actif et possibilité de supprimer proprement cette entrée.

### 10. Isolation et non-régression

1. Noter l’état et le contenu des stacks avant le test.
2. Effectuer import, fetch, pull et suppression locale.
3. Parcourir Monitor, Files local/SSH, détails/Exec/file browser container, volumes et actions start/stop/restart.
4. Vérifier après redémarrage que `/config`, les secrets Git et tous les dépôts attendus persistent.

Résultat attendu : aucun fichier sous `/compose` n’est créé ou modifié par ce lot, aucune stack n’est redéployée et les fonctions existantes restent identiques.

## Remontée d’anomalie

Noter l’architecture, le nom du dépôt local, le type d’accès, l’état affiché, l’action, le message UI et les logs Dockman correspondants. Ne jamais joindre le PAT, une clé privée, sa passphrase, la clé maître ou une URL contenant un secret.
