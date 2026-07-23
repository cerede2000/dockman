# Git Sync — lot 6 : déploiement contrôlé après synchronisation

Ce lot ajoute un déploiement Compose **opt-in par lien et par fichier Compose**. Il reste désactivé après migration. Un import Git réussi conserve son backup puis, uniquement pour une stack sélectionnée et affectée par les fichiers modifiés, Dockman exécute `compose config`, un `compose --dry-run up`, puis le `compose up` normal. Le rollback automatique appartient au lot suivant.

## 1. Migration sans effet de bord

1. Démarrer la nouvelle image avec le `/config` du lot 5.
2. Ouvrir **Settings → Git → Folder links**.

Attendu : tous les liens gardent leur configuration et affichent l'auto-déploiement désactivé. Aucun conteneur n'est recréé au démarrage.

## 2. Activation explicite

1. Ouvrir la configuration automatique d'un lien de test.
2. Vérifier que le déploiement ne peut pas être activé sans auto-sync.
3. Activer les deux options et sélectionner exactement un fichier Compose.

Attendu : enregistrer sans cible est refusé. La cible doit appartenir aux fichiers Compose découverts pour ce lien.

## 3. Changement sans rapport avec la stack

1. Sur un lien contenant plusieurs stacks, sélectionner seulement `alpha/compose.yml`.
2. Modifier dans Git un fichier sous `beta/` puis synchroniser.

Attendu : le fichier est importé avec backup mais aucune stack n'est déployée.

## 4. Déploiement de la stack affectée

1. Modifier un fichier sous `alpha/` (Compose, `.env` ou configuration autorisée).
2. Lancer **Check and synchronize now**.

Attendu : import et backup réussissent, puis validation, dry-run et `compose up` s'enchaînent. Seule `alpha` est concernée. La réponse indique une stack déployée et l'historique affiche le commit, la cible, l'état et les logs.

## 5. Compose invalide

1. Introduire volontairement une erreur YAML/Compose sur la stack de test.
2. Synchroniser.

Attendu : le fichier est sauvegardé puis importé, mais `compose config` bloque avant toute action sur les conteneurs. L'état de déploiement est `failed`, les conteneurs existants restent actifs et l'erreur apparaît dans l'historique. Corriger Git puis relancer.

## 5 bis. Nouvelle stack créée dans Git

1. Activer **Automatically deploy newly discovered Git stacks** sur un folder link de test couvrant le dossier parent des stacks.
2. Ajouter dans Git un nouveau dossier `nouvelle-stack/` contenant au minimum un `compose.yml` valide.
3. Commit, push, puis lancer la synchronisation.

Attendu : Dockman reconnaît uniquement le fichier Compose réellement ajouté par ce commit, importe le dossier, valide puis dry-run la nouvelle stack, la déploie et ajoute `nouvelle-stack/compose.yml` aux cibles autorisées persistantes. Les modifications suivantes sous ce dossier redéploient cette stack normalement.

Refaire avec l'option désactivée. Attendu : le dossier est importé mais la stack n'est ni démarrée ni ajoutée aux cibles de déploiement.

Créer ensuite 11 nouveaux dossiers de stack dans un seul commit. Attendu : l'automatisation refuse le lot avant import avec la limite de 10 ; aucune des nouvelles stacks n'est déployée. Un fichier nommé autrement que `compose.yml`, `compose.yaml`, `docker-compose.yml` ou `docker-compose.yaml` n'est jamais reconnu automatiquement comme point de déploiement.

## 6. Verrouillage concurrent

1. Lancer une action Compose longue sur la stack depuis Monitor.
2. Déclencher simultanément sa synchronisation Git.

Attendu : une seule action s'exécute. L'autre est refusée proprement avec « another action is already running », sans commandes Compose concurrentes.

## 7. Conflit et dépôt dangereux

Rejouer un conflit de fichier, puis un dépôt dirty/diverged.

Attendu : l'import complet étant bloqué, validation, dry-run et déploiement ne démarrent jamais.

## 8. Hôte SSH et redémarrage

Répéter un déploiement sur une stack de test SSH, puis redémarrer Dockman.

Attendu : le même pipeline et les mêmes verrous s'appliquent. La sélection persiste. Une opération interrompue est journalisée en échec et n'est pas présentée comme réussie.
