# Cahier de test — stockage Git compact

## Objectif

Vérifier que Dockman conserve un seul magasin d'objets Git par dépôt, sans copie persistante des fichiers de stacks, tout en préservant les imports, exports, conflits et synchronisations automatiques.

La migration est automatique au premier accès au dépôt. Elle refuse toute suppression si l'ancien espace Git contient une modification, un fichier non suivi ou un fichier ignoré.

## 1. Contrôle après mise à jour

1. Noter la taille actuelle de `/config/git/repositories`.
2. Déployer l'image de cette branche sans supprimer `/config`.
3. Ouvrir **Settings → Git**.
4. Attendre le chargement de l'état des dépôts ou cliquer sur **Fetch**.
5. Vérifier que chaque dépôt affiche le badge **compact**.
6. Vérifier dans les logs la présence, une seule fois par ancien dépôt, de `Migrated Git repository to compact object storage`.

Résultat attendu : les dépôts et folder links existants sont conservés. La taille disque baisse après la suppression des anciens checkouts, sans hausse durable de RAM ou de CPU.

## 2. Lecture Git → Dockman sans checkout permanent

1. Modifier un fichier Compose ou de configuration sur GitHub.
2. Dans Dockman, lancer **Fetch**, puis **Pull**.
3. Ouvrir la preview **Git → Dockman**.
4. Vérifier le contenu, le statut et la comparaison du fichier.
5. Importer le fichier.

Résultat attendu : la preview et l'import fonctionnent comme avant. Aucun fichier du dépôt n'est extrait durablement sous `/config/git/repositories/<id>/` ; seuls les métadonnées `.git` y restent.

## 3. Export Dockman → Git et nettoyage temporaire

1. Modifier un fichier de stack depuis Dockman.
2. Ouvrir la preview **Dockman → Git**.
3. Saisir un message de commit et exporter.
4. Vérifier sur GitHub le commit et son contenu.
5. Vérifier qu'aucun dossier `.dockman-export-*` ne reste dans `/config/git/repositories` après l'opération.

Résultat attendu : un checkout borné existe uniquement pendant l'export, puis il est supprimé, succès ou erreur compris.

## 4. Conflits et résolution partielle

1. Modifier différemment le même fichier dans Dockman et sur GitHub.
2. Lancer la synchronisation et ouvrir le conflit.
3. Comparer les deux versions.
4. Résoudre uniquement ce fichier en choisissant Git ou Dockman.
5. Laisser un autre conflit en attente si plusieurs conflits existent.

Résultat attendu : la comparaison, la résolution partielle et la baseline restent cohérentes avec le comportement précédent.

## 5. Synchronisation automatique et déploiement contrôlé

1. Activer la synchronisation automatique sur un folder link de test.
2. Modifier un fichier sur GitHub.
3. Attendre le cycle automatique.
4. Vérifier l'import, le backup et, si activé, le déploiement contrôlé.
5. Vérifier qu'une erreur reste consultable et qu'aucun checkout temporaire ne subsiste.

Résultat attendu : aucun travail Git périodique supplémentaire n'est ajouté. Le stockage compact n'occupe ni RAM ni CPU entre deux synchronisations.

## 6. Protection anti-perte lors d'une migration

Ce test est optionnel et doit utiliser un dépôt de test uniquement.

1. Avant de démarrer la nouvelle image, ajouter manuellement un fichier non validé dans l'ancien clone géré par Dockman.
2. Démarrer Dockman et demander l'état ou un Fetch sur ce dépôt.
3. Vérifier que la migration est refusée avec une erreur indiquant des changements ou des données non suivies/ignorées.
4. Vérifier que le fichier est toujours présent.
5. Sauvegarder ou supprimer volontairement ce fichier, puis relancer l'opération.

Résultat attendu : Dockman ne supprime jamais silencieusement une donnée locale qui ne correspond pas exactement au commit Git.

## 7. Suppression et recréation

1. Supprimer un folder link de test, puis le recréer.
2. Vérifier les previews et la baseline.
3. Supprimer ensuite le dépôt, uniquement quand aucun folder link ne l'utilise.
4. Vérifier que son magasin d'objets est supprimé et que les autres dépôts restent intacts.
5. Recréer le dépôt avec une URL acceptée par Dockman et relier le folder link.

Résultat attendu : le cycle complet fonctionne sans doublon de fichiers, sans résidu temporaire et sans impact sur les autres dépôts.
