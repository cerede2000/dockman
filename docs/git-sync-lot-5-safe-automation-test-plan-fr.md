# Git Sync — lot 5 : automatisation Git → Dockman sécurisée

Ce lot ajoute une surveillance périodique **optionnelle par lien de dossier**. Elle récupère les changements Git, met à jour le clone géré en fast-forward, puis importe uniquement les fichiers autorisés dans Dockman avec un backup. Elle ne supprime aucun fichier absent de Git, ne pousse jamais vers Git, et ne déploie, ne recompose ni ne redémarre aucune stack.

## Préparation

1. Démarrer l'image `ghcr.io/cerede2000/dockman:git-sync-lot-5` avec le même `/config` et la même clé Git que pour le lot 4.
2. Conserver `DOCKMAN_GIT_SYNC=true` et `DOCKMAN_GIT_MASTER_KEY_FILE=/run/secrets/dockman_git_key`.
3. Utiliser le dépôt GitHub jetable et une stack sans enjeu de production.
4. Vérifier que le lien possède déjà une baseline saine du lot 4. Sinon, confirmer une première synchronisation manuelle sans conflit.

Attendu : la migration démarre sans perte de dépôt, de lien, de règle d'inclusion/exclusion ni de baseline. Tous les liens existants affichent l'automatisation sur `off`.

## 1. Activation et validation de la fréquence

1. Dans **Settings → Git → Folder links**, ouvrir la configuration automatique du lien.
2. Activer **Synchronize changes from Git automatically**.
3. Essayer `4` minutes, puis `5` minutes.

Attendu : `4` est refusé ; `5` est accepté. La fréquence maximale est 1 440 minutes. Le lien passe à l'état `watching`, sans transfert immédiat déclenché par le bouton d'enregistrement.

## 2. Détection périodique réelle

1. Modifier dans GitHub un fichier YAML/JSON inoffensif déjà synchronisé.
2. Ne cliquer sur aucun bouton de fetch, pull ou import dans Dockman.
3. Attendre au maximum une minute pour le premier contrôle d'un lien qui n'a jamais été vérifié automatiquement.

Attendu :

- Dockman fetch et pull le commit en fast-forward ;
- le fichier local est mis à jour ;
- un backup est créé avant l'écriture ;
- l'état devient `up to date` et la date du dernier contrôle apparaît ;
- la stack et les conteneurs ne sont ni redémarrés ni recréés ; leur uptime ne repart pas à zéro.

## 3. Contrôle manuel « maintenant »

1. Faire un second changement Git valide.
2. Cliquer sur l'icône de rafraîchissement à côté de l'état automatique.

Attendu : la vérification démarre immédiatement, le bouton reste bloqué pendant l'opération et un message indique le nombre de fichiers importés. Un nouveau clic après convergence répond que la stack correspond déjà à Git.

Le commit traité est mémorisé par lien. Tant que le HEAD Git ne change pas, Dockman saute l'inventaire des fichiers : le contrôle périodique reste un fetch/statut léger, y compris avec plusieurs milliers de fichiers.

## 4. Conflit local/Git

1. Partir d'une baseline à jour.
2. Modifier différemment le même fichier dans Dockman et dans GitHub.
3. Attendre le contrôle ou lancer **maintenant**.

Attendu : l'état devient `conflict`. **Aucun fichier du lot n'est importé**, même si d'autres changements Git étaient sans conflit. Le fichier Dockman reste intact. La résolution se fait avec la comparaison et les décisions unitaires du lot 4, puis un nouveau contrôle peut être lancé.

## 5. Dépôt dans un état dangereux

Tester séparément un clone géré sale, en avance ou divergent.

Attendu : l'état devient `blocked` ou `error`, le motif est visible au survol/dans la configuration, et aucun fichier de stack n'est modifié. Dockman n'effectue jamais de merge, force-push ou choix de conflit automatique.

## 6. Règles de sélection et secrets

1. Ajouter dans Git un fichier autorisé, un `.env`, un fichier exclu et un fichier surdimensionné.
2. Lancer le contrôle.

Attendu : seules les règles persistantes du lien s'appliquent. L'opt-in sensible ponctuel de la fenêtre manuelle n'est jamais réutilisé par l'automatisation. Les secrets, fichiers spéciaux et fichiers de plus de 100 MiB restent protégés.

## 7. Non-suppression

1. Supprimer un fichier dans Git tout en le conservant dans Dockman.
2. Synchroniser automatiquement.

Attendu : le fichier local est conservé. Le mode de suppression reste volontairement non destructif.

## 8. Persistance et arrêt

1. Redémarrer Dockman avec l'automatisation activée.
2. Vérifier l'état, la fréquence et les dates.
3. Désactiver l'automatisation, puis effectuer un nouveau commit Git et attendre plus d'une minute.

Attendu : la configuration survit au redémarrage. Après désactivation, l'état revient à `off` et aucun contrôle ni import automatique n'a lieu.

## 9. Hôte SSH

Répéter un import automatique sans conflit puis un conflit sur une stack de test d'un hôte SSH connecté.

Attendu : mêmes backups, confinement de chemins, états et blocages que pour l'hôte local. Une indisponibilité SSH produit un état d'erreur lisible et ne déclenche aucune action Docker.

## Journal d'opérations

Ouvrir l'historique du dépôt.

Attendu : les contrôles automatiques apparaissent comme `auto_sync`, avec les opérations techniques fetch/pull/import associées. Une interruption due au redémarrage de Dockman est marquée en échec au démarrage suivant.
