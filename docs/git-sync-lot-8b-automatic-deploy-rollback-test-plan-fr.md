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

## 14. Pause globale d'un folder link

1. Sur un folder link dont la synchronisation automatique est active, cliquer sur **Pause** dans sa ligne sous **Settings → Git**.
2. Vérifier que le badge du lien indique `paused` et que les indicateurs des stacks signalent la pause globale.
3. Attendre au moins un intervalle configuré après avoir publié une modification saine sur Git.
4. Vérifier que cette modification n'est ni importée ni déployée automatiquement.
5. Utiliser **Check and synchronize now** pendant la pause.

Résultat attendu : la pause conserve l'intervalle, la sélection des stacks, l'auto-deploy et le rollback. Le scheduler ignore complètement ce lien, mais le contrôle manuel reste utilisable et suit le processus normal complet.

## 15. Reprise et contrôle immédiat

1. Remettre le folder link en pause puis publier une nouvelle modification saine sur Git.
2. Cliquer sur **Reprendre** dans **Settings → Git**.
3. Vérifier que la modification est traitée immédiatement sans attendre le prochain intervalle.
4. Publier une seconde modification et vérifier qu'elle est ensuite traitée au prochain intervalle normal.
5. Refaire le test avec un conflit ou un Compose invalide.

Résultat attendu : la reprise lance d'abord le cycle complet standard (fetch, comparaison, conflit/transfert, validation et éventuel déploiement), puis réactive la planification. Une erreur reste visible dans son état normal et ne provoque ni double exécution ni boucle de retry.

## 16. Pause depuis la vue Files

1. Ouvrir un vrai dossier qui est lui-même la racine d'un folder link.
2. Cliquer son indicateur Git agrégé puis **Pause folder link**.
3. Vérifier que toutes ses stacks reflètent la pause globale et que leurs sélections individuelles ne changent pas.
4. Cliquer **Resume & check now** depuis la même popup.
5. Tester ensuite un lien qui cible directement la racine complète des stacks Dockman.

Résultat attendu : l'action globale n'est proposée que sur le vrai dossier racine lié. Les dossiers parents purement agrégés restent non destructifs. Pour la racine complète des stacks, aucun faux dossier n'est créé : l'action reste disponible uniquement dans **Settings → Git**.

## 17. Réconciliation après correction Git identique au rollback

1. Après un rollback réussi, vérifier que Git contient encore la version fautive et que Dockman a restauré la version saine.
2. Corriger Git dans un nouveau commit avec exactement le contenu déjà restauré localement.
3. Lancer **Sync now** depuis la popup du dossier racine lié.

Résultat attendu : Dockman indique que Git et la stack sont identiques, clôture l'incident actif `rolled_back`, remet la synchronisation à `up_to_date` et l'auto-deploy à `watching`. L'ancien échec reste consultable uniquement dans Activity et Recent controlled deployments.

## 18. Sauvegarde des réglages sans acquittement d'incident

1. Placer volontairement un folder link en `partial`, `blocked`, `conflict` ou en erreur de déploiement.
2. Ouvrir sa configuration Git automatique sans modifier les options.
3. Cliquer **Save**.

Résultat attendu : l'état, le message et le détail de l'incident restent inchangés. Seule une vraie synchronisation/résolution réussie, ou la désactivation explicite de l'automatisation concernée, peut le clôturer.

## 19. Container en boucle de redémarrage

1. Utiliser une stack protégée par rollback avec `restart: always` ou `restart: unless-stopped`.
2. Publier sur Git une modification de Compose ou de configuration qui fait quitter le processus immédiatement avec un code non nul.
3. Déclencher la synchronisation.

Résultat attendu : pendant la fenêtre bornée `docker compose up --wait --wait-timeout 60`, le container reste `restarting` et le déploiement échoue. Dockman restaure les fichiers pré-import, redéploie la version précédente et attend son retour running/healthy. Un crash différé après la réussite de cette fenêtre nécessite un healthcheck représentatif pour être détecté de manière fiable.

## 20. Suppression déclarative protégée

1. Créer dans une stack un fichier `obsolete.conf` et un dossier `old-data` contenant un fichier et un sous-dossier vide.
2. Ajouter dans `provision.yml` :

```yaml
version: 1
remove:
  - path: obsolete.conf
    type: file
  - path: old-data
    type: directory
    recursive: true
```

3. Publier le commit et lancer la synchronisation contrôlée.
4. Vérifier dans **Backups** la création préalable d'un backup `pre_provision_delete`, téléchargeable et soumis à la rétention habituelle.
5. Vérifier que le fichier et le dossier ont disparu après un déploiement sain.
6. Refaire le scénario avec une configuration qui fait échouer le déploiement lorsque le rollback automatique est actif.

Résultat attendu : aucune suppression ne commence si l'archive complète ne peut pas être créée. Pendant l'opération, les éléments sont mis en quarantaine sur le même filesystem sans copie en mémoire. Un succès purge la quarantaine et conserve le backup ; un échec restaure exactement fichiers, dossiers vides, modes et propriétaires depuis la quarantaine. Les liens symboliques, fichiers spéciaux, chemins hors stack, fichiers Compose/manifestes, types incorrects et dossiers non vides sans `recursive: true` sont refusés avant toute mutation.

7. Retirer les cibles localement puis relancer le même manifeste.

Résultat attendu : une cible déjà absente est un succès idempotent et ne crée aucun backup vide.
