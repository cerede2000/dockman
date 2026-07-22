# Cahier de test — Git sync, lots 0 et 1

## Périmètre livré

Cette version livre le socle persistant multi-repo et la gestion sécurisée des identifiants Git. Elle ne clone, ne pousse et ne déploie encore aucune stack : ces actions arrivent au lot 2.

Image de test prévue : `ghcr.io/cerede2000/dockman:git-sync-lot-0-1`

## Préparation recommandée

Générer une clé de chiffrement dédiée :

```sh
openssl rand -base64 32 > dockman-git-master.key
chmod 600 dockman-git-master.key
```

Ajouter à la définition Compose de Dockman :

```yaml
services:
  dockman:
    image: ghcr.io/cerede2000/dockman:git-sync-lot-0-1
    environment:
      DOCKMAN_GIT_SYNC: "true"
      DOCKMAN_GIT_MASTER_KEY_FILE: /run/secrets/dockman_git_key
    secrets:
      - dockman_git_key

secrets:
  dockman_git_key:
    file: ./dockman-git-master.key
```

Conserver les volumes, le socketproxy, les origines et tous les autres paramètres actuels. Ne pas remplacer le volume `/config` pendant le test.

Pour un essai non critique, `DOCKMAN_GIT_MASTER_KEY_FILE` peut être omis. Dockman génère alors `/config/git/master.key` en `0600`, dans un dossier `0700`. Ce mode est pratique mais la clé doit impérativement être sauvegardée avec `/config`.

## Tests fonctionnels

### 1. Fonction désactivée par défaut

1. Démarrer l’image sans `DOCKMAN_GIT_SYNC`.
2. Ouvrir **Settings → Git**.
3. Vérifier que l’écran indique que la fonction est désactivée.
4. Vérifier que les vues Monitor, Files, Containers, Volumes et Settings existantes restent utilisables.

Résultat attendu : aucune clé n’est créée et aucun comportement existant ne change.

### 2. Activation et persistance

1. Activer les deux variables de la préparation recommandée et recréer Dockman.
2. Ouvrir **Settings → Git**.
3. Vérifier que l’écran affiche une liste vide et le bouton **Add credential**.
4. Redémarrer Dockman, puis revenir sur cet écran.

Résultat attendu : démarrage sans erreur de migration, écran toujours disponible.

### 3. Accès public

1. Ajouter un identifiant de type **Public / no authentication**, par exemple `github-public`.
2. Saisir l’URL d’un dépôt public sous la forme `https://github.com/owner/repository.git` dans le champ de test.
3. Cliquer sur l’action de test.
4. Modifier le nom, enregistrer, puis redémarrer Dockman.

Résultat attendu : test positif, nom modifié et conservé après redémarrage.

### 4. Token GitHub HTTPS

1. Créer un fine-grained PAT GitHub limité au dépôt de test et aux permissions de contenu nécessaires.
2. Ajouter un identifiant **GitHub HTTPS token** sans mettre le token dans l’URL.
3. Tester sans URL : GitHub doit accepter le token.
4. Tester avec l’URL du dépôt privé : le dépôt doit être joignable.
5. Modifier uniquement le nom en laissant le champ du nouveau token vide.
6. Tester à nouveau.

Résultat attendu : le secret existant est conservé lors de l’édition et n’est jamais réaffiché.

### 5. Clé SSH

1. Ajouter une clé privée SSH de test, avec sa passphrase si elle est chiffrée.
2. Saisir l’URL SSH `git@github.com:owner/repository.git`, puis lancer le test.
3. Refaire le test avec une mauvaise passphrase.

Résultat attendu : la bonne clé est validée et le dépôt est joignable. La mauvaise passphrase est refusée. La clé d’hôte présentée par GitHub est comparée à celles publiées par son API HTTPS ; aucune désactivation de cette vérification n’est utilisée.

### 6. Suppression et validation des entrées

1. Tenter de créer deux identifiants avec le même nom.
2. Tenter une URL contenant des identifiants, une query string, un autre domaine ou ne finissant pas par `.git`.
3. Supprimer un identifiant, annuler une première fois puis confirmer.

Résultat attendu : doublon et URL non sûre refusés, annulation sans effet, suppression persistante après redémarrage.

## Tests de sécurité et de reprise

### 7. Absence de secret dans le navigateur

1. Ouvrir les outils de développement, onglet Network.
2. Lister puis modifier un identifiant.
3. Examiner les réponses `/api/protected/git/credentials`.

Résultat attendu : aucune réponse ne contient le token, la clé privée ou la passphrase. Seuls `hasSecret` et un indicateur non sensible sont visibles.

### 8. Chiffrement au repos

1. Arrêter Dockman et sauvegarder `/config`.
2. Examiner `dockman.db` et ses fichiers WAL sans partager leur contenu.
3. Rechercher une chaîne unique contenue dans le token de test.

Résultat attendu : la chaîne en clair est absente. La charge utile est chiffrée en AES-256-GCM et liée à l’identifiant interne de l’entrée.

### 9. Clé incorrecte ou perdue

1. Sauvegarder la bonne clé.
2. Démarrer temporairement Dockman avec une autre clé de 32 octets.
3. Tester un identifiant existant.
4. Restaurer immédiatement la bonne clé.

Résultat attendu : déchiffrement refusé proprement, aucune donnée supprimée ou réécrite, fonctionnement restauré avec la bonne clé.

### 10. Non-régression

Effectuer au minimum : changement d’hôte, navigation et édition Files locale/SSH, Monitor, détail container, Exec selon la politique configurée, file browser container/volume, actions start/stop/restart et Settings Dockman.

Résultat attendu : aucun changement fonctionnel hors du nouvel onglet Git.

## Remontée de test

Noter pour chaque anomalie : architecture (`amd64` ou `arm64`), version affichée, type d’identifiant, dépôt public/privé, étape exacte, message UI et logs Dockman sans jamais joindre de token ni de clé privée.
