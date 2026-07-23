# Prochain lot Git — sélection, suppressions et stockage

## Objectif

Faire évoluer les folder links vers une synchronisation sélective et explicite des stacks, tout en traitant les suppressions Git sans risque de perte locale ou d'arrêt involontaire d'une production.

## 1. Sélection des stacks d'un folder link

- Afficher toutes les stacks détectées sous le dossier lié.
- Permettre la sélection et la désélection en masse avec **Tout** et **Rien**.
- Ajouter une recherche et des filtres.
- Conserver la sélection quand le filtre ou la page change : une stack cochée reste cochée après une nouvelle recherche.
- Enregistrer la sélection côté serveur ; elle ne doit pas dépendre de l'état temporaire du navigateur.
- Signaler séparément les stacks nouvellement découvertes sur Git et celles qui ne sont plus présentes sur Git.

## 2. Suppressions Git

Comportement par défaut : aucune suppression locale automatique.

Une stack supprimée sur Git devient **Deleted on Git / Local orphan** avec trois décisions possibles :

- restaurer la stack vers Git ;
- archiver localement après backup ;
- supprimer localement après confirmation.

Politiques configurables par folder link :

- **Preserve deletions**, valeur par défaut ;
- **Archive Git deletions**, mode automatique recommandé ;
- **Mirror Git deletions**, mode explicite avancé.

La suppression des fichiers et l'arrêt Docker restent deux décisions distinctes. Un `compose down` automatique est désactivé par défaut, ne supprime jamais les volumes et exige une option dédiée.

## 3. Emplacement du stockage Git

- Ajouter `DOCKMAN_GIT_STORAGE_PATH` avec l'argument équivalent `--gitStoragePath`.
- Valeur par défaut : `<CONFIG>/git`, afin de conserver la compatibilité actuelle.
- Les dépôts sont placés sous `<GIT_STORAGE_PATH>/repositories` et les backups sous `<GIT_STORAGE_PATH>/backups`.
- Accepter un volume Docker dédié, sécuriser les permissions et refuser les chemins de dépôt qui se chevauchent avec les dossiers de stacks.
- Documenter la migration : le changement de variable ne déplace jamais silencieusement les données existantes.

## 4. Colonne Compose files

- Afficher uniquement le nombre de stacks sélectionnées dans la table des folder links.
- Un clic ouvre une fenêtre compacte contenant la liste complète.
- Ajouter recherche, filtres, sélection persistante, **Tout/Rien** et actions groupées.
- Permettre de retirer une stack de la synchronisation sans supprimer ses fichiers.
- Distinguer clairement : sélectionnée, disponible, nouvelle sur Git, absente de Git et orpheline locale.

## Garde-fous

- Backup obligatoire avant toute modification ou suppression locale.
- Aucun volume Docker supprimé.
- Aucun arrêt ou redéploiement implicite.
- Preview et audit des décisions destructrices.
- Limites de fichiers et traitement par flux conservés.
