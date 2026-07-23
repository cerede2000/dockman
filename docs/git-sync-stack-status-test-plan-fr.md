# Cahier de test — statut Git par stack

## 0. Préparation

- Utiliser l’image de la branche `feat/git-sync-compact-storage`.
- Conserver au moins un folder link contenant trois stacks sélectionnées.
- Activer la synchronisation automatique sur ce folder link.
- Activer l’auto-déploiement sur une seule des trois stacks.
- Ouvrir Dockman dans deux onglets : **Files** et **Monitor**.

## 1. Affichage nominal

1. Vérifier qu’une icône de synchronisation apparaît uniquement sur les stacks liées à Git.
2. Dans **Files**, replier puis déplier les dossiers parents.
3. Dans **Monitor**, vérifier que l’icône est située entre la case à cocher et le nom de la stack.
4. Cliquer sur l’icône dans les deux vues.

Résultat attendu : le même panneau compact affiche le dépôt, la branche, le chemin Git, les dernières dates, le prochain contrôle, le commit, l’état de l’automatisation et celui du déploiement. Une stack non liée ne reçoit aucune icône.

## 2. États et différenciation visuelle

Vérifier successivement :

- vert : stack synchronisée ;
- orange : changements locaux ou Git en attente ;
- bleu animé : contrôle en cours ;
- rouge : conflit, erreur de synchronisation ou échec de déploiement ;
- gris : état initial ou automatisation en pause.

Une petite horloge doit distinguer l’auto-sync. Une petite fusée doit distinguer l’auto-déploiement.

## 3. Modification locale événementielle

1. Modifier puis sauvegarder un fichier de configuration d’une seule stack depuis **Files**.
2. Ne pas lancer de preview et ne pas attendre le polling.

Résultat attendu : seule cette stack passe immédiatement en orange. Les autres restent vertes. Un contrôle automatique sans nouveau commit Git ne doit pas remettre cette stack au vert.

## 4. Changement Git et contrôle manuel

1. Modifier sur Git un fichier appartenant à une autre stack.
2. Dans le panneau de cette stack, cliquer sur **Check now**.
3. Vérifier le résultat dans **Files** et **Monitor**.

Résultat attendu : le contrôle utilise la mécanique d’auto-sync existante, met à jour uniquement les états concernés et ne déclenche pas d’action sur une stack en pause.

## 5. Conflit et accès direct

1. Modifier différemment le même fichier dans Dockman et dans Git.
2. Lancer le contrôle automatique.
3. Cliquer sur l’icône rouge, puis sur **Resolve conflicts**.

Résultat attendu : Dockman ouvre directement l’onglet Git, la fenêtre de résolution du bon folder link et filtre la liste sur le dossier de la stack. La comparaison et les choix **Keep Git** / **Keep Dockman** restent disponibles conflit par conflit.

## 6. Pause par stack

1. Mettre une seule stack en pause depuis son panneau.
2. Créer un changement Git sur la stack en pause et un autre sur une stack active.
3. Lancer la synchronisation.

Résultat attendu : la stack en pause reste intacte et grise ; la stack active est traitée normalement.

4. Mettre toutes les stacks du folder link en pause.
5. Attendre un cycle automatique.

Résultat attendu : le cycle est ignoré proprement, sans erreur ni transfert. Si la découverte et l’auto-déploiement de nouvelles stacks sont activés, une nouvelle stack Git peut toujours être découverte ; les stacks déjà en pause restent exclues.

6. Reprendre une stack et relancer un contrôle.

Résultat attendu : cette stack redevient éligible sans modifier la sélection permanente du folder link.

## 7. Auto-déploiement

1. Modifier sur Git la stack autorisée à être auto-déployée.
2. Lancer ou attendre la synchronisation.
3. Ouvrir son panneau pendant puis après l’opération.

Résultat attendu : les étapes de validation puis le résultat du déploiement sont visibles. Un échec rend l’icône rouge et affiche le détail ; une autre stack non autorisée n’est pas déployée.

## 8. Dossiers parents et stacks imbriquées

1. Utiliser une stack placée dans plusieurs sous-dossiers.
2. Créer un conflit ou une erreur sur cette stack.
3. Replier toute l’arborescence.

Résultat attendu : les dossiers parents signalent l’anomalie même repliés. Ils ne dupliquent pas les indicateurs verts en régime nominal.

## 9. Isolation par hôte

1. Basculer sur un autre hôte Docker.
2. Revenir sur l’hôte initial.

Résultat attendu : aucun état Git d’un hôte ne s’affiche sur l’autre ; les panneaux et les actions ciblent toujours le bon hôte.

## 10. Contrôle d’overhead

1. Laisser **Files** ouvert cinq minutes avec une arborescence volumineuse et sans action.
2. Relever CPU et RAM du container Dockman.
3. Refaire la mesure dans **Monitor**.

Résultat attendu : une seule lecture compacte des statuts a lieu toutes les 30 secondes par hôte visible, quelle que soit la quantité de lignes. Aucun scan des fichiers ou du dépôt Git n’est lancé pour afficher les icônes. Le CPU doit revenir à son niveau de repos entre les rafraîchissements et la RAM ne doit pas croître au fil du temps.
