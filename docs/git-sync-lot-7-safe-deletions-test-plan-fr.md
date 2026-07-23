# Lot 7 — Suppressions Git sûres

Ce cahier valide que la disparition d'un fichier ou d'une stack sur Git ne provoque jamais une suppression locale silencieuse. Il doit être exécuté sur un dépôt et des stacks de test.

## 1. Préparation

1. Déployer l'image de test du lot.
2. Créer un folder link contenant au moins deux stacks dans des sous-dossiers distincts, par exemple `alpha/compose.yml` et `beta/compose.yml`.
3. Dans `alpha`, ajouter un fichier de configuration synchronisable, un `.env` factice et un fichier d'un type exclu de la synchronisation.
4. Effectuer une première synchronisation et vérifier que le lien et les deux stacks sont au vert.
5. Activer la synchronisation automatique avec un intervalle court uniquement pour ces tests.

Résultat attendu : Dockman et Git possèdent une base commune, sans conflit.

## 2. Suppression distante d'un fichier isolé

1. Supprimer sur Git uniquement le fichier de configuration de `alpha`, sans supprimer son Compose.
2. Lancer une vérification Git → Dockman.
3. Ouvrir la prévisualisation Git → Dockman.

Résultats attendus :

- le fichier est indiqué `deleted on Git` et reste présent localement ;
- il ne peut pas être sélectionné comme un transfert ordinaire ;
- la stack indique des changements locaux en attente, pas une stack orpheline ;
- aucun conteneur n'est arrêté ou redéployé ;
- un push Dockman → Git permet de restaurer le fichier.

## 3. Stack entièrement supprimée de Git

1. Supprimer sur Git tout le dossier `alpha`, puis valider le commit.
2. Attendre le polling ou utiliser `Check now`.
3. Contrôler Settings > Git, Files et Monitor.

Résultats attendus :

- le lien passe en état bloqué et la stack en état `Deleted on Git · preserved locally` ;
- l'indicateur agrégé de ses dossiers parents reflète l'avertissement ;
- le dossier local `alpha` et tous ses fichiers sont toujours présents ;
- les conteneurs restent dans leur état précédent ;
- aucun `compose down`, aucune suppression de conteneur et aucune suppression de volume n'ont lieu ;
- cliquer sur l'état bloqué ou sur l'indicateur de stack ouvre la vue utile ;
- la prévisualisation propose `Restore to Git`, `Archive local` et `Delete local` sur le Compose de la stack complète.

## 4. Polling sans surcoût continu

1. Sans modifier Git après le test précédent, laisser passer au moins trois intervalles automatiques.
2. Observer l'état et l'utilisation CPU de Dockman.

Résultats attendus :

- l'état reste bloqué/orphelin et ne repasse pas faussement au vert ;
- le message indique que le scan de stack est ignoré lorsqu'aucun nouveau commit Git n'existe ;
- les polls suivants effectuent seulement le contrôle Git léger ;
- il n'y a pas de croissance continue de la RAM ni de hausse durable du CPU.

## 5. Restauration vers Git

1. Depuis la popup de statut ou la prévisualisation, choisir `Restore to Git`.
2. Confirmer l'action.
3. Inspecter le dépôt distant.

Résultats attendus :

- le Compose et les fichiers autorisés de `alpha` reviennent dans Git ;
- le `.env`, les secrets et les types exclus ne sont pas ajoutés ;
- le dossier local n'est pas modifié ;
- après le prochain contrôle, la stack revient à l'état synchronisé.

## 6. Archivage local

1. Supprimer de nouveau tout `alpha` sur Git et attendre sa détection.
2. Choisir `Archive local`.
3. Vérifier que l'action reste impossible tant que la phrase `REMOVE LOCAL ORPHAN` n'est pas saisie exactement.
4. Confirmer.

Résultats attendus :

- une archive est créée sous le stockage Git configuré, dans `backups/archives/<binding-id>/` ;
- l'archive contient l'intégralité du dossier local, y compris le `.env` et le type de fichier exclu de Git ;
- le dossier local est retiré seulement après la création et la relecture cohérente de la sauvegarde ;
- la stack est retirée de la sélection et du catalogue du folder link ;
- les conteneurs et volumes existants ne sont pas touchés ;
- cette archive n'est pas supprimée par la rotation normale des cinq sauvegardes de synchronisation.

## 7. Suppression locale explicite

1. Recréer et resynchroniser une stack de test, puis supprimer tout son dossier sur Git.
2. Choisir `Delete local`, saisir la phrase de confirmation et valider.

Résultats attendus :

- une sauvegarde tournante est créée avant le retrait local ;
- le dossier local est retiré ;
- aucun Compose down et aucune action Docker destructive ne sont exécutés ;
- les conteneurs et volumes restent présents ;
- les sauvegardes ordinaires conservent la rétention configurée.

## 8. Protections contre les mauvaises suppressions

Vérifier séparément les cas suivants :

1. Modifier localement un fichier après sa suppression sur Git : un conflit `deleted on Git · local changed` doit apparaître et rien ne doit être écrasé.
2. Garder un fichier de la stack ouvert avec des modifications non enregistrées : archive et suppression doivent être refusées.
3. Ne supprimer sur Git qu'une partie du dossier en y laissant un autre fichier : les actions de retrait de stack complète ne doivent pas être proposées et le serveur doit les refuser.
4. Utiliser une stack placée directement à la racine du folder link : la restauration est autorisée, mais le retrait automatique du dossier racine est refusé.
5. Placer une seconde stack sous le dossier candidat : le retrait du dossier parent est refusé.
6. Ajouter avant l'action un lien symbolique, un fichier spécial, un fichier supérieur à 100 Mio, plus de 20 000 fichiers ou plus de 2 Gio au total : Dockman doit refuser l'action et ne rien supprimer.
7. Modifier un fichier pendant la création de la sauvegarde : la comparaison finale doit refuser le retrait. La sauvegarde déjà créée reste récupérable.

## 9. Nettoyage et hôtes distants

1. Répéter au moins un archivage sur un host SSH.
2. Vérifier le contenu de l'archive et l'absence d'action Docker destructive.
3. Supprimer ensuite le folder link : ses sauvegardes tournantes et ses archives doivent être nettoyées.
4. Refaire le test en supprimant le dépôt configuré : le même nettoyage doit avoir lieu pour ses liens.

## Critères de validation du lot

Le lot est validé si aucune suppression Git ne supprime silencieusement une donnée locale, si toute suppression locale proposée est précédée d'une sauvegarde complète et d'une confirmation explicite, et si aucun conteneur ou volume Docker n'est modifié par ces décisions.
