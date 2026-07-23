# Cahier de test — lot 6 Git : sélection des stacks et stockage dédié

## 1. Objectifs du lot

Ce lot doit valider ensemble les comportements suivants :

1. la colonne **Compose files** affiche un nombre compact et ouvre le détail au clic ;
2. les stacks d'un folder link peuvent être sélectionnées ou retirées de la synchronisation en masse ;
3. la recherche et le filtre de statut ne perdent jamais les sélections faites précédemment ;
4. le périmètre peut être choisi dès la création du folder link, avant une éventuelle initialisation Dockman → Git ou Git → Dockman ;
5. les cibles d'auto-déploiement disposent de la même sélection en masse ;
6. une stack non sélectionnée n'est ni lue, ni hachée, ni copiée, ni déployée par ce lien ;
7. les liens créés avant cette version continuent à synchroniser toutes leurs stacks ;
8. les objets Git compacts et les sauvegardes peuvent être placés dans un volume dédié avec `DOCKMAN_GIT_STORAGE_PATH` ;
9. aucun scan supplémentaire n'est ajouté au polling de fond.

## 2. Préparation et sauvegarde

Utiliser l'image de la branche indiquée dans le compte-rendu de livraison. Conserver :

- `DOCKMAN_GIT_SYNC=true` ;
- `DOCKMAN_GIT_MASTER_KEY_FILE=/run/secrets/dockman_git_key` ;
- le même `/config` et le même dossier de stacks que pendant les tests précédents.

Avant la mise à jour :

1. arrêter Dockman proprement ;
2. sauvegarder `/config`, le secret Git et le dossier de stacks ;
3. noter un folder link existant, son dépôt, le nombre de fichiers Compose et son état de synchronisation ;
4. redémarrer avec la nouvelle image, sans ajouter la nouvelle variable de stockage pour le premier parcours.

Résultat attendu : Dockman démarre, la migration de base est automatique et aucun fichier de stack ou dépôt distant n'est modifié au démarrage.

## 3. Compatibilité d'un folder link existant

1. Ouvrir **Settings > Git**.
2. Repérer le folder link noté avant la mise à jour.
3. Vérifier que la colonne **Compose files** contient un nombre.
4. Cliquer sur ce nombre.
5. Vérifier que toutes les stacks déjà connues sont marquées **synchronized**.
6. Fermer avec **Cancel**, puis effectuer un preview Dockman → Git et Git → Dockman.

Résultats attendus :

- le nombre correspond au nombre de stacks sélectionnées ;
- toutes les stacks historiques restent sélectionnées ;
- les previews ont le même périmètre qu'avant la mise à jour ;
- aucune resélection manuelle n'est nécessaire.

## 4. Fenêtre de détail et sélection simple

1. Cliquer sur le nombre de fichiers Compose.
2. Vérifier le titre, le chemin du folder link, la recherche, le filtre **Status**, les boutons de sélection et le compteur `sélectionnées / détectées`.
3. Décocher une stack avec sa case.
4. Recocher cette stack avec le bouton `+` de sa ligne.
5. La retirer à nouveau avec la corbeille de sa ligne.
6. Cliquer sur **Cancel**, rouvrir la fenêtre et vérifier que la modification non sauvegardée n'a pas été conservée.
7. Refaire la modification et cliquer sur **Save selection**.
8. Recharger complètement la page puis rouvrir la fenêtre.

Résultats attendus :

- toute la ligne reste lisible et la liste seule défile si elle est longue ;
- `Cancel` n'enregistre rien ;
- `Save selection` conserve le choix après rechargement et après redémarrage de Dockman ;
- retirer une stack ne supprime aucun fichier local ou Git et n'arrête aucun conteneur.

## 5. Sélection en masse, recherche et mémorisation

Utiliser de préférence un folder link contenant au moins six stacks avec des noms distincts.

1. Cliquer sur **Select none** : le compteur doit passer à `0 / N` et un avertissement doit apparaître.
2. Cliquer sur **Select all** : le compteur doit repasser à `N / N`.
3. Rechercher un mot ne correspondant qu'à deux ou trois stacks.
4. Cliquer sur **Deselect filtered**.
5. Remplacer la recherche par un autre mot et sélectionner une autre stack.
6. Effacer la recherche.
7. Vérifier que les choix des deux recherches sont toujours présents.
8. Choisir le statut **Not selected**, vérifier la liste, puis utiliser la case de l'en-tête pour sélectionner uniquement les éléments visibles.
9. Choisir le statut **Selected**, vérifier le résultat.
10. Sauvegarder, recharger la page et vérifier l'ensemble des choix.

Résultats attendus :

- changer de recherche ou de filtre ne réinitialise jamais les choix ;
- **Select/Deselect filtered** ne touche qu'au résultat filtré ;
- **Select all/none** agit sur tout le catalogue, même si un filtre est actif ;
- la case d'en-tête agit sur la liste filtrée ;
- le compteur est exact en permanence.

## 6. Isolation Dockman → Git

Préparer deux stacks `stack-a` et `stack-b`, puis ne sélectionner que `stack-a`.

1. Modifier un fichier de configuration autorisé dans les deux stacks.
2. Lancer le preview Dockman → Git.
3. Vérifier qu'aucun fichier de `stack-b` n'apparaît, pas même comme fichier skipped.
4. Valider l'export et consulter le commit Git.

Résultats attendus : seuls les fichiers de `stack-a` sont lus, affichés et poussés. Le contenu Git de `stack-b` ne change pas.

### 6.1 Sélection dès la création du lien

Préparer un dossier source détecté contenant au moins `stack-a`, `stack-b` et `stack-c`.

1. Cliquer sur **Link folder** et choisir ce dossier source.
2. Dans **Stacks included in this link**, rechercher `stack-b`, puis cliquer sur **Deselect filtered**.
3. Rechercher `stack-c`, la décocher avec sa case, puis effacer la recherche.
4. Vérifier que seule `stack-a` reste sélectionnée et que les choix faits sous les filtres sont conservés.
5. Choisir **Dockman → Git** comme initialisation et créer le lien.
6. Contrôler le commit et le dépôt distant.
7. Rouvrir le nombre de Compose files du lien.

Résultats attendus :

- le lien est créé avec uniquement `stack-a` synchronisée ;
- l'initialisation pousse uniquement l'arborescence de `stack-a` ;
- `stack-b` et `stack-c` ne sont ni lues ni ajoutées au commit ;
- la sélection enregistrée est identique après rechargement et redémarrage.

Refaire ensuite le test avec **Git → Dockman** : seules les stacks sélectionnées doivent être importées et sauvegardées. Enfin, créer un lien avec **Select none** et **Link only** : le lien doit être accepté avec `0 / N`, sans transfert ni suppression.

## 7. Isolation Git → Dockman

Conserver uniquement `stack-a` sélectionnée.

1. Modifier sur Git un fichier de `stack-a` et un fichier de `stack-b` dans le même commit.
2. Faire fetch/pull si nécessaire puis ouvrir le preview Git → Dockman.
3. Vérifier que seul le changement de `stack-a` est proposé.
4. Importer.
5. Contrôler les deux dossiers locaux.

Résultats attendus : `stack-a` reçoit la modification avec une sauvegarde ; `stack-b` reste strictement inchangée.

## 8. Retrait puis réactivation d'une stack

1. Retirer `stack-b` de la sélection et sauvegarder.
2. Modifier `stack-b` localement et sur Git pendant qu'elle est désactivée.
3. Vérifier que les previews du lien l'ignorent.
4. Réactiver `stack-b` et sauvegarder.
5. Relancer les deux previews.

Résultats attendus : la stack redevient visible. Si les deux côtés ont divergé depuis leur dernier état connu, le conflit normal est présenté ; aucune version n'est écrasée automatiquement.

## 9. Sélection vide

1. Utiliser **Select none**, sauvegarder puis recharger la page.
2. Lancer les deux previews et un contrôle automatique manuel si l'auto-sync est activé.
3. Vérifier les stacks et le dépôt.

Résultats attendus : le folder link est conservé, aucun fichier n'est transféré, aucun conteneur n'est redéployé et la sélection vide persiste.

## 10. Interaction avec l'auto-sync et l'auto-déploiement

1. Ouvrir la configuration automatique d'un lien contenant au moins six stacks.
2. Activer la synchronisation puis **Deploy affected stacks after a successful import**.
3. Vérifier la présence de la recherche, du filtre de statut, des actions **Select all/none**, **Select/Deselect filtered**, du compteur et de la case d'en-tête.
4. Rechercher `stack-a`, la sélectionner, rechercher `stack-b`, la sélectionner, puis effacer la recherche.
5. Vérifier que les deux sélections ont été mémorisées ; choisir **Selected** puis **Not selected** et contrôler chaque liste.
6. Utiliser **Select filtered** sur un autre résultat, sauvegarder, recharger la page puis rouvrir la configuration.
7. Vérifier que toutes les cibles choisies ont persisté.
8. Retirer ensuite `stack-b` de la sélection générale de synchronisation et sauvegarder.
9. Rouvrir la configuration automatique.
10. Vérifier que `stack-b` n'est plus une cible autorisée et n'est plus proposée dans la liste.
11. Modifier `stack-b` sur Git et lancer un contrôle automatique.

Résultats attendus : les filtres ne perdent aucune sélection, les actions en masse respectent leur périmètre, les choix persistent après sauvegarde, `stack-b` n'est ni importée ni déployée et `stack-a` continue de fonctionner normalement.

Test complémentaire « nouvelle stack Git » :

1. conserver une sélection explicite ;
2. activer **Automatically deploy newly discovered Git stacks** ;
3. ajouter une nouvelle stack valide sur Git ;
4. lancer la synchronisation automatique.

Résultat attendu : la nouvelle stack est importée, validée, déployée puis ajoutée à la fois au catalogue, à la sélection et aux cibles autorisées. Les limites et contrôles de sécurité déjà présents restent appliqués.

## 11. Stockage historique sans variable

Avec `DOCKMAN_GIT_STORAGE_PATH` absent :

1. redémarrer Dockman sur le `/config` existant ;
2. vérifier que les dépôts sont toujours présents ;
3. faire fetch, preview et une petite synchronisation ;
4. vérifier que les sauvegardes continuent à être créées et nettoyées selon la rétention existante.

Résultat attendu : les chemins historiques `/config/git/repositories` et `/config/git/backups` restent utilisés. Il n'y a aucune migration ou copie implicite.

## 12. Nouveau stockage Git dédié

Pour un environnement de test neuf, ajouter :

```yaml
services:
  dockman:
    environment:
      DOCKMAN_GIT_STORAGE_PATH: /git-data
    volumes:
      - dockman_git_data:/git-data

volumes:
  dockman_git_data:
```

Puis :

1. redémarrer Dockman ;
2. ajouter un dépôt et un folder link ;
3. effectuer fetch, export, import et création d'une sauvegarde ;
4. redémarrer à nouveau ;
5. vérifier que le dépôt, le lien et les opérations fonctionnent encore.

Résultats attendus :

- les objets Git compacts sont sous `/git-data/repositories` ;
- les sauvegardes sont sous `/git-data/backups` ;
- la base Dockman, les identifiants chiffrés et la clé maître restent gérés via `/config` et le secret ;
- aucune copie complète supplémentaire des stacks n'est créée ;
- le volume dédié persiste après recréation du conteneur.

## 13. Déplacement contrôlé d'un stockage existant

La nouvelle variable ne déplace volontairement rien toute seule.

1. arrêter Dockman ;
2. copier, sans les modifier, le contenu actuel de `/config/git/repositories` vers `<nouveau-volume>/repositories` et celui de `/config/git/backups` vers `<nouveau-volume>/backups` ;
3. conserver la sauvegarde de `/config` ;
4. monter le nouveau volume sur `/git-data` et définir `DOCKMAN_GIT_STORAGE_PATH=/git-data` ;
5. redémarrer et tester fetch, status et preview ;
6. ne supprimer l'ancien stockage qu'après validation complète.

Résultat attendu : tous les dépôts restent reconnus. En cas d'erreur, arrêter Dockman, retirer la variable et remonter le `/config` sauvegardé pour revenir au chemin historique.

## 14. Validation des chemins et permissions

1. Essayer temporairement `DOCKMAN_GIT_STORAGE_PATH=git-data` : Dockman doit refuser de démarrer avec une erreur indiquant qu'un chemin absolu est requis.
2. Essayer temporairement `/` : Dockman doit refuser ce chemin trop large.
3. Tester un chemin absolu monté en lecture seule : Dockman doit échouer explicitement lors de l'accès au dépôt, sans basculer silencieusement sur `/config`.
4. Restaurer `/git-data` en lecture/écriture.

## 15. Charge CPU, RAM et disque

1. Mesurer `docker stats dockman` pendant dix minutes sans ouvrir l'onglet Git.
2. Ouvrir/fermer plusieurs fois le détail des stacks, rechercher et filtrer une grande liste sans sauvegarder.
3. Sauvegarder une sélection puis laisser Dockman au repos dix minutes.
4. Lancer ensuite une synchronisation et observer le retour au niveau de repos.
5. Comparer la taille du volume Git avant/après avec la taille du dossier de stacks.

Résultats attendus :

- aucun nouveau scan de stacks n'est déclenché par le polling de fond ;
- recherche et filtre sont locaux à l'interface ;
- une stack décochée n'est pas hachée pendant les transferts ;
- la mémoire transitoire est libérée après synchronisation ;
- le dépôt compact ne contient pas un second checkout complet des stacks.

## 16. Non-régression finale

Valider au minimum : ajout/test d'un credential, copie d'URL, fetch/pull/push, exclusions globales, preview dans les deux sens, comparaison et résolution d'un conflit, backup/restauration, auto-réconciliation, auto-sync, auto-déploiement contrôlé, delink/relink et redémarrage de Dockman.

## 17. Cohérence entre l'éditeur et la synchronisation Git

Ce parcours se réalise idéalement avec deux fenêtres : la première sur l'éditeur Dockman, la seconde sur **Settings > Git**. Utiliser un folder link contenant au moins deux stacks `stack-a` et `stack-b`.

### 17.1 Fichier ouvert mais non modifié

1. Ouvrir `stack-a/compose.yml` dans l'éditeur et placer le curseur au milieu du fichier.
2. Modifier ce fichier sur Git, puis lancer Git → Dockman ou attendre l'auto-sync.
3. Revenir dans l'éditeur sans recharger le navigateur.

Résultats attendus : le contenu se rafraîchit automatiquement, le curseur reste sur une position valide, aucun brouillon local n'est créé et la stack peut être auto-déployée normalement si cette option est active.

### 17.2 Édition active pendant une importation

1. Préparer sur Git une modification de `stack-a/compose.yml` et une autre de `stack-b/compose.yml` dans le même commit.
2. Dans Dockman, commencer à modifier `stack-a/compose.yml` et continuer à saisir du texte afin que l'état **Typing** reste actif.
3. Depuis la seconde fenêtre, déclencher immédiatement la synchronisation Git → Dockman.
4. Contrôler les deux stacks et le résultat de synchronisation.

Résultats attendus : `stack-a` n'est jamais écrasée et n'est pas auto-déployée ; `stack-b` est synchronisée et peut être déployée ; l'état du lien signale qu'une stack est temporairement bloquée par un éditeur. Aucun polling ou scan supplémentaire n'est lancé.

### 17.3 Comparaison et choix explicite

1. Pendant qu'un brouillon local de `stack-a` existe, faire arriver une version Git différente ou provoquer une sauvegarde sur une révision devenue obsolète.
2. Vérifier l'avertissement dans l'éditeur et ouvrir **Compare**.
3. Vérifier les deux colonnes : brouillon Dockman à gauche, fichier courant à droite.
4. Choisir **Keep editing** : la fenêtre se ferme, le brouillon reste visible et l'avertissement reste présent.
5. Rouvrir la comparaison et choisir **Use current file**.

Résultats attendus : la version Git remplace le brouillon uniquement après cette décision explicite, l'avertissement disparaît et l'auto-sync peut reprendre.

Refaire le scénario et choisir **Overwrite with my draft**. Résultat attendu : Dockman sauvegarde explicitement le brouillon par-dessus la révision courante, sans boucle d'erreurs, puis la modification apparaît comme changement local à pousser vers Git.

### 17.4 Protection entre deux navigateurs

1. Ouvrir le même fichier dans deux navigateurs ou deux sessions distinctes.
2. Modifier et laisser sauvegarder la première session.
3. Modifier ensuite la copie devenue obsolète dans la seconde session.

Résultat attendu : la seconde sauvegarde reçoit un conflit, n'écrase pas la première, présente la comparaison et exige **Use current file** ou **Overwrite with my draft**.

### 17.5 Expiration et fermeture de l'éditeur

1. Commencer une modification, puis fermer brutalement l'onglet du navigateur avant la sauvegarde.
2. Attendre deux minutes et relancer la synchronisation.
3. Refaire le test en naviguant proprement vers un autre fichier après sauvegarde.

Résultats attendus : un bail abandonné expire automatiquement et ne bloque jamais durablement Git ; une sauvegarde réussie ou la fermeture normale libère immédiatement le blocage ; aucune entrée persistante n'est ajoutée en base.

### 17.6 Charge au repos

1. Fermer tous les éditeurs, puis mesurer le CPU pendant dix minutes.
2. Ouvrir un fichier sans le modifier et mesurer à nouveau.
3. Modifier puis sauvegarder, attendre deux minutes et contrôler CPU/RAM.

Résultats attendus : le flux d'événements reste dormant sans modification, aucun timer serveur de polling n'est créé, le seul renouvellement navigateur est actif uniquement pendant un brouillon sale, et Dockman revient à son niveau de repos habituel.

Le lot est accepté si tous les résultats attendus sont obtenus, si aucun fichier non sélectionné n'est modifié et si l'utilisation CPU au repos ne présente pas de hausse durable par rapport à l'image précédente.
