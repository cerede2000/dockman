# Cahier de test — Updates lot 6 : durcissement opérationnel

## Préparation

1. Déployer l'image `integration` livrée avec ce lot.
2. Ouvrir **Updates** et conserver au moins deux cibles inscrites, idéalement une stack de plusieurs conteneurs et un conteneur autonome.
3. Vérifier que les actions déjà validées restent disponibles : scan en lecture seule, politiques, SMTP, mise à jour protégée et historique.

## 1. Pause globale persistante

1. Cliquer sur **Pause auto**.
2. Vérifier la présence du bandeau orange indiquant que seules les installations sont suspendues.
3. Lancer **Scan enrolled** : le scan doit fonctionner et ne modifier aucun conteneur.
4. Tenter **Run updates now** : le bouton doit être désactivé.
5. Redémarrer Dockman et rouvrir la vue : la pause doit être conservée.
6. Cliquer sur **Resume auto** : le bandeau disparaît et les exécutions redeviennent disponibles.

## 2. Cycle automatique immédiat

1. Préparer une cible inscrite dont une nouvelle image est disponible.
2. Cliquer sur **Run updates now**.
3. Vérifier que la boîte de confirmation explique clairement qu'il s'agit d'une exécution et non d'un scan.
4. Valider puis suivre les conteneurs : la même vérification de santé et le même rollback que pour une échéance planifiée doivent s'appliquer.
5. Vérifier la nouvelle entrée dans **Automatic update history**, portant le marqueur `manual`.
6. Vérifier la notification SMTP d'exécution si elle est activée.

## 3. Navigation pendant une exécution

1. Lancer un cycle immédiat sur une image suffisamment longue à télécharger.
2. Quitter la page Updates puis y revenir.
3. L'opération ne doit pas être annulée par la navigation du navigateur.
4. Une seconde tentative simultanée doit être refusée ; un seul cycle par hôte est autorisé.

## 4. Limite anti-effet de masse

1. Dans **Scheduled checks**, ouvrir **Limits**.
2. Saisir `1` groupe par cycle et enregistrer.
3. Préparer simultanément une stack multi-conteneurs et un conteneur autonome avec mises à jour disponibles.
4. Lancer un cycle immédiat.
5. Vérifier qu'un seul groupe est traité. Si la stack est choisie, tous ses membres éligibles doivent être traités dans la même transaction : elle ne doit jamais être coupée par la limite.
6. Relancer un cycle pour traiter le groupe restant.
7. Remettre la limite à `0` et vérifier que l'interface indique **sans limite**.

## 5. Reprise après redémarrage forcé

Ce test est volontairement perturbateur ; utiliser une cible de test avec rollback actif.

1. Lancer un cycle immédiat.
2. Pendant son exécution, redémarrer brutalement uniquement le conteneur Dockman.
3. Après redémarrage, ouvrir **Automatic update history**.
4. L'exécution interrompue doit être terminée explicitement avec le message indiquant que Dockman a redémarré avant d'enregistrer le résultat.
5. L'exécution automatique globale doit être passée en pause de sécurité.
6. Contrôler manuellement l'état réel de la cible, puis utiliser **Resume auto**.
7. Vérifier qu'aucune boucle ou tâche fantôme ne reste active.

## 6. Non-régression et charge de fond

1. Vérifier qu'une mise à jour planifiée normale fonctionne encore.
2. Vérifier une transaction de stack réussie, puis un rollback provoqué.
3. Vérifier que le circuit breaker bloque toujours le même digest après échec et que **Retry** le libère.
4. Laisser Dockman au repos au moins dix minutes : le lot n'ajoute aucun polling. La consommation CPU de fond doit rester comparable à celle observée avant le lot.
5. Vérifier que l'historique reste borné et que les anciens runs continuent d'être nettoyés comme auparavant.

## Résultat attendu

Le lot est validé si pause/reprise persistent, le cycle manuel utilise exactement les protections automatiques, la limite ne scinde jamais une stack, une interruption est visible après redémarrage et aucune régression n'apparaît sur les lots 1 à 5.
