# Lot 7 — Historique, sauvegardes et restauration Git

Ce cahier valide le lot sur une instance de test. Une restauration ne doit jamais déployer, redémarrer ou arrêter une stack.

## 0. Préparation

1. Utiliser l'image construite depuis `feat/git-sync-recovery-activity`.
2. Sauvegarder `/config`, le volume des stacks et le volume Git avant le test.
3. Conserver la configuration Git existante et, si un volume Git dédié est utilisé, `DOCKMAN_GIT_STORAGE_PATH`.
4. Les nouvelles rétentions sont facultatives :
   - `DOCKMAN_GIT_HISTORY_RETENTION_DAYS=30` ;
   - `DOCKMAN_GIT_BACKUP_RETENTION_DAYS=30`.
5. Les valeurs acceptées vont de 1 à 3650 jours. Une valeur invalide doit arrêter Dockman avec une erreur explicite.

Résultat attendu : Dockman démarre, applique la migration une seule fois et retrouve tous les dépôts/folder links, sélections, états et baselines existants.

## 1. Contrôle de non-régression immédiat

1. Ouvrir Files, Monitor puis Settings > Git.
2. Vérifier les bullets Docker et Git, y compris les dossiers imbriqués repliés.
3. Ouvrir un fichier Compose, le modifier, enregistrer puis contrôler l'état `Local changes waiting`.
4. Exécuter un preview Stack → Git et Git → Stack sans valider de transfert.
5. Laisser une synchronisation automatique s'exécuter.

Résultat attendu : aucune page blanche, aucun polling supplémentaire visible, aucune perte de sélection et aucun déploiement déclenché par un simple preview.

## 2. Historique d'un folder link

1. Dans Settings > Git > Folder links, cliquer sur l'icône History.
2. Vérifier que la fenêtre affiche date, action, origine, stack, statut et détail.
3. Réaliser successivement : modification de politique, pause/reprise d'une stack, import manuel et synchronisation automatique.
4. Rafraîchir l'historique.

Résultat attendu :

- les actions manuelles portent l'origine `manual` ;
- l'auto-sync, l'import automatique et l'auto-deploy portent `automation` ;
- un échec contient un message exploitable ;
- l'historique est propre au folder link sélectionné.

## 3. Accès depuis Files et Monitor

1. Dans Files, cliquer sur l'indicateur Git d'une stack précise.
2. Ouvrir Activity puis Backups depuis la popup.
3. Refaire le test depuis l'indicateur de la ligne stack dans Monitor.
4. Cliquer sur l'indicateur agrégé d'un dossier parent.

Résultat attendu : Activity et Backups ouvrent la même vue depuis Files, Monitor et Settings. L'indicateur parent reste un résumé et ne lance aucune action aveugle sur une stack arbitraire.

## 4. Création et identification d'une sauvegarde

1. Modifier sur Git un fichier déjà présent localement et ajouter un second fichier de configuration.
2. Faire Preview Git → Stack puis importer.
3. Ouvrir Backups sur le folder link.

Résultat attendu : une sauvegarde `pre import` apparaît avec sa date, le nombre de fichiers, la taille, les stacks concernées, le commit et l'expiration. Le transfert reste fonctionnel et la stack n'est pas déployée automatiquement hors politique d'auto-deploy explicitement active.

## 5. Téléchargement d'une sauvegarde

1. Cliquer sur Download.
2. Ouvrir l'archive `.tar.gz` hors de Dockman.
3. Vérifier la présence du manifeste `.dockman-backup-manifest.json` et des anciennes versions des fichiers remplacés.

Résultat attendu : l'archive est lisible, le manifeste décrit les états avant/après et aucun chemin absolu ou sortant du folder link n'est présent.

## 6. Restauration sûre complète

1. Depuis la sauvegarde du test 4, cliquer sur Restore.
2. Vérifier la prévisualisation fichier par fichier.
3. Laisser toutes les actions sûres sélectionnées et confirmer.
4. Contrôler les fichiers, l'état Git, les containers et l'historique.

Résultat attendu :

- les fichiers modifiés retrouvent leur ancienne version ;
- les fichiers ajoutés par l'import sont supprimés ;
- une sauvegarde `pre restore` est créée avant écriture ;
- l'état devient `Local changes waiting` ;
- aucun container n'est redémarré et aucun déploiement Compose n'est exécuté ;
- l'action apparaît dans Activity.

## 7. Conflit apparu après la sauvegarde

1. Refaire un import qui crée une sauvegarde.
2. Modifier ensuite localement un des fichiers sauvegardés.
3. Ouvrir la prévisualisation de restauration.
4. Restaurer uniquement les autres fichiers encore sûrs.

Résultat attendu : le fichier retouché est marqué `conflict`, non sélectionnable et jamais écrasé. Les autres fichiers peuvent être restaurés indépendamment.

## 8. Prévisualisation devenue obsolète

1. Ouvrir une prévisualisation de restauration sans la fermer.
2. Modifier sur disque un fichier sélectionné.
3. Confirmer l'ancienne prévisualisation.

Résultat attendu : Dockman refuse l'opération et demande une nouvelle prévisualisation. Le fichier récent reste intact.

## 9. Éditeur avec changements non enregistrés

1. Ouvrir dans l'éditeur un fichier appartenant à la sauvegarde et le modifier sans enregistrer.
2. Tenter la restauration.

Résultat attendu : la restauration est refusée tant que l'éditeur concerné est sale. Aucun fichier n'est écrit.

## 10. Archive d'une stack supprimée sur Git

1. Supprimer une stack sur Git, synchroniser, puis choisir Archive pour l'orpheline.
2. Vérifier que la stack locale disparaît et qu'une sauvegarde `orphan archive` apparaît.
3. Restaurer cette sauvegarde.

Résultat attendu : le dossier et ses fichiers sont recréés sans déploiement. Le catalogue Compose est rafraîchi une fois et la stack réapparaît comme changement local à revoir, sans être sélectionnée silencieusement si elle avait été retirée de la sélection.

## 11. Suppression volontaire d'une sauvegarde

1. Cliquer sur Delete pour une sauvegarde non nécessaire.
2. Annuler une première fois, puis confirmer.

Résultat attendu : seule l'archive choisie disparaît. Les stacks, le dépôt, l'historique Git et les autres sauvegardes restent intacts.

## 12. Nettoyage au delink et à la suppression du dépôt

1. Créer plusieurs sauvegardes sur un folder link de test.
2. Retirer le folder link.
3. Vérifier le stockage Git dédié, puis supprimer le dépôt depuis Dockman.

Résultat attendu : les sauvegardes du link sont nettoyées au delink. Aucun répertoire d'archive orphelin ne subsiste après retrait du dépôt. Les stacks locales ne sont pas supprimées par le delink.

## 13. Rétention en nombre et en jours

1. Générer plus de dix sauvegardes sur le même folder link par des imports successifs.
2. Rafraîchir Backups.
3. Pour un test accéléré de durée, démarrer une instance jetable avec `DOCKMAN_GIT_BACKUP_RETENTION_DAYS=1` et des métadonnées de sauvegarde antérieures à un jour, puis redémarrer.

Résultat attendu : au maximum dix sauvegardes actives sont conservées par folder link et les archives expirées sont supprimées au démarrage/à la maintenance quotidienne. L'historique et les déploiements plus vieux que leur rétention sont également purgés.

## 14. Verrouillage des opérations concurrentes

1. Lancer une synchronisation automatique ou un transfert Git long.
2. Tenter simultanément une restauration.

Résultat attendu : la restauration refuse proprement de démarrer si l'auto-sync ou le dépôt est occupé. Aucun état partiel n'est créé.

## 15. CPU, RAM et disque

1. Laisser Dockman inactif au moins quinze minutes, sans fenêtre Activity/Backups ouverte.
2. Observer CPU et RAM, puis ouvrir/fermer plusieurs fois la fenêtre de récupération.
3. Contrôler la taille du stockage Git et des sauvegardes après plusieurs opérations.

Résultat attendu : aucune nouvelle boucle de polling, aucun CPU de fond supplémentaire mesurable et retour de la RAM vers son niveau habituel après fermeture. Les listes sont chargées uniquement à l'ouverture et la maintenance s'exécute au plus une fois par jour.

## 16. Validation finale

Valider un parcours complet : création/édition Files, sync manuelle, sync auto, conflit, résolution, suppression Git préservée, suppression locale explicite, auto-deploy contrôlé, Monitor, logs et actions containers.

Résultat attendu : aucune régression fonctionnelle sur les lots précédents et aucune action Docker déclenchée par l'historique, le téléchargement ou la restauration d'une sauvegarde.

## 17. Vérification et création d'une branche distante

1. Ajouter un dépôt existant en indiquant une branche déjà présente.
2. Vérifier que l'import se termine directement, sans confirmation supplémentaire.
3. Recommencer avec un autre dépôt ou après retrait du premier, en indiquant une branche absente, par exemple `dockman`.
4. Vérifier que Dockman indique clairement que la branche n'existe pas et précise la branche distante qui servira de point de départ.
5. Annuler : vérifier qu'aucune branche distante et qu'aucun dépôt local Dockman ne sont créés.
6. Relancer puis confirmer `Create branch and import`.
7. Vérifier sur GitHub que la branche `dockman` existe et pointe initialement sur le même commit que la branche de départ.
8. Vérifier que le dépôt est ensuite importé dans Dockman avec l'état `ready`.
9. Refaire le test avec un dépôt public sans identifiant d'écriture.

Résultat attendu : Dockman ne crée jamais une branche silencieusement. Sans droit d'écriture, le message explique que la création automatique est impossible. Une branche vide ou un nom Git invalide est refusé proprement, sans clone local résiduel.
