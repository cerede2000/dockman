# Cahier de test — lot 8B : rollback automatique après échec de déploiement

Ce lot protège les déploiements automatiques Git → Dockman. La protection est désactivée par défaut et n'ajoute aucun polling permanent. Lorsqu'elle est activée, Dockman attend que Compose confirme l'état `running/healthy`. Si la nouvelle version échoue, seuls les fichiers de la stack concernée sont restaurés depuis le backup pré-import, puis l'ancienne version est remise en service et contrôlée.

## 0. Préparation

1. Utiliser l'image construite depuis `feat/git-sync-recovery-activity`.
2. Préparer un folder link contenant au moins deux stacks de test, toutes deux synchronisées et vertes.
3. Activer la synchronisation automatique et le déploiement automatique sur ces deux stacks.
4. Laisser **Restore the previous stack automatically...** désactivé pour le premier test.

Résultat attendu : l'option de rollback est désactivée après migration et les automatismes existants conservent leur comportement.

## 1. Activation explicite et persistance

1. Ouvrir les options d'automatisation du folder link.
2. Activer le rollback automatique, enregistrer puis rouvrir la fenêtre.
3. Désactiver le déploiement automatique.

Résultat attendu : l'option reste cochée après sauvegarde. Désactiver le déploiement désactive aussi le rollback ; il est impossible d'avoir un rollback automatique sans auto-sync et auto-deploy.

## 2. Déploiement sain sans régression

1. Modifier sur Git l'image ou un fichier de configuration d'une stack avec une valeur valide.
2. Attendre le prochain cycle ou utiliser **Check now**.
3. Observer la stack, son activité et les déploiements récents.

Résultat attendu : import avec backup, validation, dry-run, `compose up`, attente de l'état running/healthy, puis état vert `success`. Aucun second déploiement n'est exécuté.

## 3. Échec avant modification de Docker

1. Provoquer un échec de validation ou de dry-run après import (par exemple une variable requise indisponible au moment de la commande). Un Compose syntaxiquement invalide reste, lui, bloqué avant import comme dans le lot 6.
2. Lancer la synchronisation.

Résultat attendu : Docker n'est pas modifié. Les fichiers importés appartenant à cette stack reviennent à leur version précédente. Le déploiement apparaît en jaune `rolled back`, avec l'erreur d'origine et les journaux copiables.

## 4. Échec de démarrage ou de healthcheck

1. Sur Git, publier une configuration syntaxiquement valide mais dont le service ne peut pas démarrer, ou un healthcheck qui reste en échec.
2. Lancer la synchronisation et attendre au maximum 60 secondes.
3. Vérifier le fichier local, les containers et l'indicateur Git.

Résultat attendu :

- la nouvelle version est tentée une seule fois ;
- l'échec déclenche la restauration ciblée des fichiers pré-import ;
- la configuration restaurée est revalidée et passée en dry-run ;
- l'ancienne version est redéployée puis attendue running/healthy ;
- la stack reste fonctionnelle sur l'ancienne version ;
- l'indicateur est jaune et indique **Deployment failed · previous version restored** ;
- **Recent controlled deployments** affiche `rolled back` et conserve l'erreur initiale.

## 5. Isolation entre stacks

1. Dans un même commit, casser le démarrage de la stack A et apporter une modification saine à la stack B.
2. Lancer la synchronisation.

Résultat attendu : A est restaurée automatiquement et B est déployée normalement. Les locks, fichiers, backups et commandes Compose ne se croisent jamais. Le résultat global est `partial`, pas un abandon du lot au premier échec.

## 6. Protection d'une modification concurrente

1. Provoquer un déploiement lent ou en échec.
2. Modifier le fichier concerné entre son import et la tentative de rollback, depuis Dockman ou l'hôte.

Résultat attendu : Dockman refuse d'écraser cette version inattendue. L'état devient rouge `rollback failed`, le fichier récent reste intact et l'interface demande une récupération manuelle via Backups ou Commits.

## 7. Éditeur non enregistré

1. Ouvrir un fichier de la stack dans l'éditeur et conserver une modification non enregistrée.
2. Déclencher un scénario qui demanderait un rollback automatique.

Résultat attendu : le rollback refuse toute écriture sur cette stack. Le détail indique qu'un éditeur non enregistré bloque la récupération ; aucune autre stack n'est affectée.

## 8. Échec de l'ancienne version restaurée

1. Construire un scénario de test où la nouvelle version échoue et où l'ancienne version restaurée ne peut plus démarrer non plus (dépendance externe absente, par exemple).
2. Lancer la synchronisation.

Résultat attendu : état rouge `rollback failed`, conservation de l'erreur initiale et de l'erreur de récupération, backup toujours disponible, aucune boucle de retry automatique et aucune fausse indication verte.

## 9. Commit Git suivant après rollback

1. Après un rollback réussi, laisser le commit fautif inchangé pendant deux intervalles.
2. Corriger ensuite la stack dans un nouveau commit Git.
3. Relancer la synchronisation.

Résultat attendu : le même commit fautif n'est pas redéployé en boucle. Le nouveau commit est comparé à la baseline restaurée, importé puis déployé normalement. Après réussite, l'indicateur redevient vert.

## 10. Historique, activité et restauration manuelle

1. Ouvrir **Activity**, **Backups** puis les détails d'automatisation.
2. Retrouver l'opération de rollback.
3. Vérifier le commit, la stack, le backup, les fichiers et les deux sorties Compose.

Résultat attendu : l'activité `stack_deploy` porte l'état `rolled_back` ou `rollback_failed`. Le backup pré-import reste restaurable manuellement et suit la politique de rétention existante.

## 11. Hôtes local et SSH

1. Refaire un déploiement sain et un rollback sur l'hôte local via socketproxy.
2. Refaire les mêmes tests sur un hôte SSH si disponible.

Résultat attendu : la même résolution de chemins et la même cascade de `.env` que les actions Compose Dockman existantes sont utilisées. Aucun `/root/.docker` inscriptible n'est requis par cette évolution.

## 12. CPU, RAM et nettoyage

1. Observer Dockman dix minutes au repos, option activée mais sans nouveau commit.
2. Exécuter plusieurs déploiements sains, puis deux rollbacks.
3. Observer ensuite la mémoire, le CPU et les PIDS.

Résultat attendu : aucun surcoût de fond mesurable, aucun nouveau goroutine/polling persistant, retour de la RAM à son niveau habituel après les transferts, et journaux bornés à la limite existante.

## 13. Régression option désactivée

1. Désactiver le rollback automatique.
2. Déployer une modification saine puis une modification dont le démarrage échoue.

Résultat attendu : la modification saine utilise le chemin historique non bloquant. L'échec conserve le comportement d'auto-deploy antérieur et ne restaure rien implicitement.
