# Cahier de test — lot 8A : rollback local vers un commit Git

Ce lot restaure explicitement des fichiers locaux depuis un ancien commit. Il ne réécrit jamais l'historique Git et n'exécute aucune action Compose ou Docker.

## 0. Préparation

1. Utiliser l'image construite depuis `feat/git-sync-recovery-activity`.
2. Choisir un folder link de test contenant au moins deux stacks synchronisées.
3. Vérifier que plusieurs commits successifs ont modifié leurs fichiers Compose ou de configuration.
4. Conserver l'auto-sync active afin de vérifier sa mise en pause après rollback.

Résultat attendu : Settings > Git > Git recovery contient un nouvel onglet **Commits**. Aucun accès à cet onglet n'ajoute de polling permanent.

## 1. Historique ciblé du folder link

1. Ouvrir Git recovery puis Commits.
2. Vérifier les SHA courts, dates, auteurs et messages.
3. Comparer avec l'historique du dépôt Git.

Résultat attendu : seuls les commits atteignables depuis la branche configurée et affectant le dossier Git lié sont proposés. Le commit courant est identifié.

## 2. Preview sans écriture

1. Choisir un ancien commit et cliquer sur Preview.
2. Vérifier les compteurs `restore`, `remove` et `skipped`.
3. Fermer la fenêtre sans confirmer.
4. Contrôler les fichiers locaux et les containers.

Résultat attendu : aucun fichier, statut, container, dépôt ou baseline n'est modifié par le preview.

## 3. Sélection de plusieurs stacks

1. Ouvrir le preview d'un commit qui affecte plusieurs stacks.
2. Décocher une stack complète.
3. Décocher ensuite un fichier individuel d'une autre stack.
4. Vérifier que les sélections des autres stacks restent mémorisées dans la fenêtre.

Résultat attendu : décocher une stack désélectionne toutes ses actions ; la sélection par fichier reste possible et aucune action hors des stacks synchronisées n'est proposée.

## 4. Comparaison avant décision

1. Sur un fichier texte modifié disponible des deux côtés, cliquer sur Compare.
2. Vérifier la version Dockman à gauche et le commit sélectionné à droite.
3. Tester YAML, JSON et un fichier texte.
4. Tester si possible un fichier binaire ou supérieur à la limite de comparaison.

Résultat attendu : Monaco affiche le diff coloré pour les textes. Un fichier non comparable affiche une explication sans charger son contenu complet en mémoire.

## 5. Rollback local sûr

1. Confirmer le rollback d'une seule stack.
2. Vérifier les fichiers restaurés et les fichiers explicitement retirés.
3. Vérifier la liste Backups et Activity.
4. Vérifier Files et Monitor.

Résultat attendu :

- une sauvegarde `pre commit rollback` est créée avant la première écriture ;
- seuls les fichiers sélectionnés changent ;
- la stack porte l'état **Local changes waiting** avec le détail `Local rollback waiting` ;
- l'auto-sync de cette stack est mise en pause ;
- aucune commande Compose, aucun restart, stop ou redeploy n'est exécuté ;
- l'activité contient le commit cible, la sauvegarde et les chemins restaurés.

## 6. Protection contre un preview périmé

1. Ouvrir un preview de rollback.
2. Modifier localement un fichier sélectionné avant de confirmer.
3. Confirmer l'ancien preview.

Résultat attendu : Dockman refuse l'opération et demande un nouveau preview. La modification récente reste intacte.

## 7. Éditeur non enregistré

1. Modifier sans enregistrer un fichier de la stack dans l'éditeur Dockman.
2. Tenter un rollback concernant cette stack.

Résultat attendu : le rollback est refusé avant toute écriture et avant toute création de backup inutile.

## 8. Commit contenant un Compose invalide

1. Sélectionner un commit historique dont le Compose est syntaxiquement invalide.
2. Ouvrir son preview puis tenter la validation.

Résultat attendu : l'erreur est visible, le bouton de rollback est bloqué et aucun fichier local n'est modifié.

## 9. Commit antérieur à la création d'une stack

1. Choisir un commit qui précède la création d'une stack sélectionnée.
2. Vérifier l'avertissement et les actions `remove`.
3. Confirmer uniquement dans un environnement de test.

Résultat attendu : les fichiers synchronisés sélectionnés sont retirés après backup, mais Dockman ne lance pas `compose down` et ne supprime aucun volume. La stack passe en suppression locale en attente.

## 10. Auto-sync après rollback

1. Attendre au moins deux intervalles d'auto-sync après un rollback.
2. Vérifier les fichiers et les containers.
3. Depuis l'indicateur de la stack, tester `Push & resume`.
4. Refaire un rollback puis tester directement `Push to Git` depuis l'indicateur.
5. Refaire un rollback, provoquer volontairement un conflit distant, puis tester la reprise.

Résultat attendu : la stack en rollback reste en pause et Git ne réapplique pas silencieusement le commit courant. Un push complet réussi ou `Push & resume` publie les changements puis reprend automatiquement l'auto-sync. Un échec ou un conflit conserve la pause. Les autres stacks non concernées continuent à se synchroniser.

## 11. Restauration du rollback

1. Ouvrir Backups.
2. Prévisualiser puis restaurer la sauvegarde `pre commit rollback`.

Résultat attendu : l'état présent avant rollback peut être récupéré avec les mêmes protections de preview et de backup de sécurité que les restaurations existantes.

## 12. CPU, RAM et concurrence

1. Fermer Git recovery et observer Dockman au repos.
2. Ouvrir plusieurs fois Activity, Backups et Commits.
3. Pendant un auto-sync, tenter un rollback.

Résultat attendu : aucun nouveau polling de fond, retour de la mémoire au niveau habituel et refus propre du rollback si le dépôt ou l'auto-sync est occupé.

## 13. Pause de récupération et pause manuelle

1. Mettre manuellement une stack en pause, modifier un fichier puis utiliser `Push to Git`.
2. Vérifier ensuite l'état de l'automatisation.
3. Restaurer un commit ou un backup, puis vérifier le libellé de la pause.

Résultat attendu : un simple push ne retire jamais une pause manuelle. Une restauration crée une pause identifiée `Paused after recovery`, que le push complet peut retirer automatiquement. Une sélection partielle de fichiers conserve la pause jusqu'à ce que tous les changements de la stack soient publiés.

## 14. Rafraîchissement des indicateurs parents

1. Faire échouer deux stacks contenues dans un même dossier parent.
2. Vérifier que les stacks et le parent deviennent rouges.
3. Redémarrer les deux stacks rapidement.
4. Observer les indicateurs sans recharger la page.

Résultat attendu : les événements Docker du burst sont regroupés jusqu'au dernier événement, les indicateurs enfants reviennent au vert et le parent recalculé revient lui aussi au vert. Aucun polling supplémentaire permanent n'est ajouté.

## 15. Suppression locale d'un fichier de configuration suivi

1. Dans une stack synchronisée et verte, créer puis synchroniser un fichier `test.conf`.
2. Supprimer `test.conf` uniquement depuis Dockman.
3. Lancer **Check now** depuis la popup Git de la stack.
4. Vérifier que l'icône devient jaune, que l'état indique une suppression locale et que la popup affiche clairement que la synchronisation automatique est bloquée.
5. Vérifier que **Check now** affiche un avertissement avec la décision attendue, et non un faux succès générique.
6. Vérifier les trois résolutions, en recréant le scénario avant chaque variante :
   - **Restore from Git** recrée `test.conf` localement et rend la synchronisation verte ;
   - **Stop synchronizing** ajoute une exclusion exacte, conserve la copie Git et rend la synchronisation stable ;
   - **Delete from Git** exige la saisie `DELETE FILE FROM GIT`, crée et pousse un commit supprimant uniquement `test.conf`, puis rend la synchronisation verte.
7. Ouvrir également **Preview Dockman → Git** : la ligne `deleted locally` doit proposer les mêmes trois décisions directement dans la colonne **Resolution**.
8. Modifier ensuite `test.conf` sur Git, supprimer sa copie locale et réessayer la suppression Git : Dockman doit refuser l'effacement et demander une comparaison/résolution de conflit.

Résultat attendu : aucune suppression distante n'est propagée silencieusement, mais chaque suppression locale suivie possède une sortie explicite et accessible depuis les deux interfaces.

## 16. Statut Docker des stacks arrêtées

1. Avec une stack en fonctionnement, vérifier que sa bullet, ses dossiers parents et sa ligne Monitor sont verts.
2. Arrêter volontairement la stack depuis Dockman.
3. Vérifier sans recharger la page que la stack devient grise dans Files et que Monitor indique `exited` avec sa couleur neutre.
4. Redémarrer la stack et vérifier que la stack et tous ses parents reviennent au vert.
5. Arrêter une seule stack dans un dossier qui en contient plusieurs encore actives.
6. Vérifier que le dossier parent devient orange, et non rouge : il représente un fonctionnement partiel.
7. Tester si possible un conteneur `unhealthy`, `dead` ou bloqué en `restarting`.

Résultat attendu : gris signifie arrêté, orange signifie partiellement actif, rouge est réservé à un échec observable et vert signifie entièrement actif. Un code de sortie `137` ou `143` ne suffit jamais à classer un arrêt comme une erreur. Files et Monitor doivent rester cohérents, y compris après un redémarrage de Dockman.
