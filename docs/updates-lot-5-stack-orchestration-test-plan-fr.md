# Lot 5 — cahier de test de l’orchestration des mises à jour par stack

## Objectif

Une politique **Entire stack** forme désormais une transaction cohérente. Dockman télécharge toutes les images nécessaires avant de modifier le premier container, respecte les relations `depends_on`, verrouille la stack pendant toute l’opération et restaure en ordre inverse les containers déjà modifiés si un membre échoue. Une politique **This container only** conserve son fonctionnement isolé.

## Préparation

1. Créer une stack de test composée au minimum de `db`, `api` et `front`.
2. Déclarer `api -> db` et `front -> api` avec `depends_on`.
3. Ajouter un healthcheck fiable sur chaque service.
4. Utiliser des tags d’images contrôlables et publier une nouvelle version saine des trois images.
5. Dans **Updates**, ouvrir un container de cette stack, choisir **Entire stack**, activer l’installation automatique et le rollback, puis utiliser un cron temporaire d’au moins 15 minutes.

## 1. Préchargement sans mutation partielle

1. Rendre volontairement inaccessible l’une des trois nouvelles images.
2. Attendre le créneau automatique.
3. Vérifier qu’aucun ID de container de la stack n’a changé.
4. Vérifier dans **Automatic update history** que l’image fautive est en échec et que les autres membres sont indiqués comme annulés avant modification.
5. Ouvrir le détail : le journal doit montrer le préchargement des images et l’arrêt avant toute recréation.

## 2. Mise à jour nominale de stack

1. Rendre toutes les nouvelles images disponibles.
2. Autoriser un nouveau test avec **Retry** si le digest précédent est encore bloqué.
3. Attendre le créneau suivant.
4. Vérifier que les trois containers sont mis à jour et sains.
5. Vérifier que l’historique affiche le nom de la stack sur chaque résultat et l’icône de transaction de stack.
6. Vérifier dans les journaux que `db` est traité avant `api`, puis `api` avant `front`.

## 3. Rollback global

1. Publier une nouvelle version saine de `db` et `api`, mais une version de `front` dont le healthcheck échoue.
2. Attendre le créneau automatique.
3. Vérifier que `front` revient à son ancienne version.
4. Vérifier que `api` puis `db`, déjà mis à jour, sont restaurés en ordre inverse.
5. Vérifier que toute la stack fonctionne avec les anciennes images.
6. Vérifier que les résultats concernés sont `rolled_back` et que le digest fautif est protégé par le coupe-circuit.

## 4. Isolation entre stacks

1. Enrôler une seconde stack saine sur le même créneau.
2. Provoquer un échec dans la première stack.
3. Vérifier que la seconde stack est tout de même mise à jour.
4. Vérifier qu’il existe des résultats distincts et lisibles pour les deux transactions.

## 5. Règle container inchangée

1. Configurer un container avec **This container only**.
2. Déclencher une nouvelle mise à jour disponible.
3. Vérifier que seul ce container est traité et que le résultat ne porte pas le badge de transaction de stack.

## 6. Verrouillage et concurrence

1. Pendant une transaction de stack, tenter un restart, un down ou une mise à jour manuelle sur cette même stack.
2. Vérifier que l’action concurrente est refusée proprement.
3. Vérifier qu’une action sur une autre stack reste possible.

## 7. Redémarrage et ressources

1. Redémarrer Dockman après un rollback global.
2. Vérifier que l’historique et les blocages sont conservés.
3. Vérifier au repos qu’aucun nouveau polling n’apparaît et que la consommation CPU reste au niveau du lot précédent.

## Critères de validation

- aucune mutation si un téléchargement préalable échoue ;
- ordre des dépendances respecté ;
- rollback global en ordre inverse ;
- aucune action concurrente sur la stack ;
- autres stacks et règles container indépendantes ;
- historique et journaux identifient clairement la transaction de stack ;
- aucun overhead de fond supplémentaire.
