# Lot 8 — cahier de test : versions disponibles et policies en masse

## Objectifs

- détecter qu'un tag de version plus récent existe sans modifier le tag configuré ;
- conserver le contrôle manuel sur les images figées (`v3`, `v3.1`, `v3.1.1`) ;
- appliquer une même policy à plusieurs conteneurs ou stacks en une seule opération atomique ;
- ne provoquer ni pull, ni redéploiement, ni surcharge permanente au repos.

## Préparation

1. Ouvrir la vue **Updates**.
2. Conserver au moins :
   - un conteneur standalone utilisant une image publique avec un tag versionné ;
   - deux conteneurs appartenant à une même stack ;
   - si possible, une seconde stack ;
   - un conteneur protégé ou piloté par labels pour vérifier qu'il n'est pas modifiable depuis l'interface.
3. Relever les tags Compose avant les tests. Ils ne doivent jamais être modifiés par ce lot.

## 1. Compatibilité et comportement par défaut

1. Charger la vue après mise à jour de Dockman.
2. Vérifier que les policies existantes sont intactes.
3. Ouvrir une policy existante.
4. Vérifier que **Version discovery** vaut **Off** par défaut.
5. Lancer **Check updates**.

Résultat attendu : le contrôle digest classique fonctionne comme avant et aucune indication de version supérieure n'apparaît pour les policies désactivées.

## 2. Policy patch

1. Choisir un conteneur utilisant un tag complet, par exemple `v3.1.1`.
2. Régler **Version discovery** sur **Patch** et laisser les préversions désactivées.
3. Enregistrer puis lancer un contrôle.

Résultat attendu : seule une version `v3.1.x` supérieure peut être proposée. Une `v3.2.x` ou `v4.x` n'est pas proposée.

Pour un tag volontairement large comme `v3`, le mode Patch reste dans la branche majeure `v3` et peut signaler le tag `v3.x.y` le plus récent.

## 3. Policies minor et major

1. Passer la policy sur **Minor**, enregistrer et contrôler.
2. Vérifier que la meilleure version de la même majeure peut être signalée.
3. Passer ensuite sur **Major**, enregistrer et contrôler.
4. Vérifier qu'une majeure supérieure peut être signalée.

Résultat attendu : l'interface affiche distinctement `tag actuel → tag disponible` et la policy utilisée.

## 4. Préversions

1. Laisser **Include prerelease tags** désactivé et contrôler une image proposant une RC/beta plus récente.
2. Vérifier que la préversion n'est pas retenue.
3. Activer l'option puis relancer le contrôle.

Résultat attendu : une préversion compatible avec la portée patch/minor/major peut désormais être signalée.

## 5. Garantie non destructive

1. Relever le tag dans le fichier Compose et l'identifiant de l'image active.
2. Lancer plusieurs contrôles jusqu'à obtenir une indication de tag plus récent.
3. Attendre également un cycle automatique si une planification est active.

Résultats attendus :

- aucun fichier Compose n'est modifié ;
- aucun conteneur n'est redéployé à cause du tag supérieur ;
- aucune image correspondant au nouveau tag n'est téléchargée ;
- le statut digest reste indépendant : une image peut être `current` tout en ayant un tag supérieur signalé.

## 6. Cache et charge

1. Affecter la découverte à plusieurs conteneurs utilisant le même dépôt d'image.
2. Lancer un contrôle, puis un second immédiatement.
3. Observer brièvement CPU, RAM et trafic vers le registry.

Résultat attendu : le catalogue est mutualisé et mis en cache pendant six heures. Le second contrôle ne doit pas multiplier les requêtes de catalogue. Aucun traitement périodique supplémentaire ne tourne hors des contrôles planifiés ou manuels.

## 7. Sélection multiple de conteneurs

1. Filtrer la liste avec la recherche.
2. Cocher plusieurs conteneurs visibles.
3. Modifier la recherche, cocher d'autres lignes, puis revenir au premier filtre.
4. Vérifier que la sélection est conservée.
5. Cliquer sur **Edit policies** et choisir **Each selected container**.
6. Définir activation, planning, découverte de version, rollback et nettoyage, puis enregistrer.
7. Réouvrir individuellement plusieurs lignes.

Résultat attendu : toutes les lignes sélectionnées possèdent exactement la nouvelle policy.

## 8. Sélection multiple de stacks

1. Sélectionner plusieurs conteneurs appartenant à une ou plusieurs stacks.
2. Ouvrir l'édition en masse et choisir **Selected complete stacks**.
3. Enregistrer la policy.

Résultats attendus :

- chaque stack n'est enregistrée qu'une fois, même si plusieurs de ses conteneurs étaient cochés ;
- tous les membres de chaque stack héritent de la même policy ;
- les anciens overrides individuels des conteneurs sélectionnés sont retirés dans la même opération ;
- si un standalone fait partie de la sélection, le mode stack est indisponible et l'interface l'explique.

## 9. Atomicité et protections

1. Vérifier qu'un conteneur piloté par labels ou protégé n'est pas sélectionnable pour une édition de policy.
2. Tenter d'enregistrer une policy en masse avec un planning invalide.
3. Réouvrir les lignes concernées.

Résultat attendu : un message d'erreur est affiché et aucune des policies de la sélection n'est partiellement modifiée.

## 10. Notification SMTP

1. Activer les notifications de mises à jour.
2. Laisser un contrôle planifié détecter un nouveau tag.
3. Vérifier le courriel reçu, puis laisser un second contrôle inchangé s'exécuter.

Résultats attendus :

- le message indique le conteneur, le tag courant, le tag supérieur et la portée de policy ;
- le message rappelle que l'information ne modifie pas le tag Compose ;
- un état identique ne génère pas de notification répétitive.

## Validation finale

Le lot est validé si la découverte reste informative, si les contrôles digest et les mises à jour existantes n'ont aucune régression, et si une policy en masse s'applique intégralement ou pas du tout.
