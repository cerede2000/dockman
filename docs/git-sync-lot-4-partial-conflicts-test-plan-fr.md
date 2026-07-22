# Git Sync lot 4 — test de la résolution partielle des conflits

Utiliser une stack et un dépôt de test avec l'image `ghcr.io/cerede2000/dockman:git-sync-lot-4`.

## 1. Conflit initial sans référence

1. Créer un nouveau folder link entre deux dossiers contenant trois fichiers de mêmes noms mais de contenus différents.
2. Ouvrir une prévisualisation dans chaque direction.

Attendu : les trois fichiers sont en `initial conflict`, jamais en simple `modify`. Aucun transfert n'est possible sans sélectionner au moins un conflit.

## 2. Comparaison

1. Cliquer sur `Compare` pour un conflit texte.
2. Parcourir les blocs colorés dans la vue Dockman/Git.
3. Tester `Leave pending`, puis rouvrir le comparateur.

Attendu : Dockman reste à gauche et Git à droite, le contenu n'est pas modifiable et fermer la comparaison ne prend aucune décision. Un binaire ou un fichier de plus de 2 MiB affiche seulement sa taille et son SHA-256.

## 3. Résolution d'un conflit sur trois

1. Choisir `Keep Dockman` ou `Keep Git` pour un seul fichier.
2. Laisser les deux autres en attente.
3. Exécuter le transfert sélectionné.
4. Relancer une prévisualisation.

Attendu : seul le conflit choisi est résolu. Les deux autres restent en conflit. Les changements sans conflit sont transférés normalement. Le message final indique le nombre de conflits encore en attente.

## 4. Annulation d'une décision

1. Approuver un conflit depuis sa ligne.
2. Cliquer sur `Pending` avant le transfert.

Attendu : le fichier redevient en attente et ne sera pas écrasé.

## 5. Choix du côté opposé

1. Dans un export Dockman vers Git, ouvrir un conflit avec `Compare`.
2. Choisir `Keep Git`.

Attendu : Dockman passe automatiquement à une prévisualisation Git vers Dockman et présélectionne uniquement ce fichier. Même si d'autres fichiers deviennent de simples modifications dans cette direction, ils ne sont pas transférés. L'inverse doit fonctionner avec `Keep Dockman` depuis un import.

## 6. Dissociation restaurable

1. Laisser deux conflits non résolus.
2. Supprimer le folder link avec `Unlink`.
3. Recréer exactement le même lien : même host, dossier local, repository et dossier Git.
4. Relancer la prévisualisation.

Attendu : les deux conflits sont toujours présents grâce à la référence SHA restaurée. Aucun fichier n'a été supprimé ou copié pendant la dissociation.

## 7. Oubli volontaire

1. Supprimer à nouveau le lien avec `Unlink and forget`.
2. Recréer le même lien.

Attendu : l'ancienne référence n'est pas restaurée. Tous les fichiers présents différemment des deux côtés sont signalés en `initial conflict` et nécessitent une décision explicite.

## 8. Persistance et sécurité

1. Laisser un conflit en attente et redémarrer Dockman.
2. Vérifier le conflit après redémarrage.
3. Tester un fichier sensible sans puis avec la confirmation dédiée.

Attendu : le conflit persiste. Le comparateur ne contourne pas la protection des fichiers sensibles. Aucune synchronisation ne déploie ou ne redémarre une stack.
