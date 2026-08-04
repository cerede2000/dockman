# Mise à jour protégée d'une infrastructure sensible — cahier de test

## Objectif

Valider qu'un service Compose dont Dockman dépend, notamment `socketproxy`, peut être mis à jour manuellement sans interrompre l'opération en cours, avec vérification du nouveau conteneur et rollback automatique.

## Préconditions

- Dockman et le service sensible sont sur l'hôte local et gérés par Docker Compose.
- Dockman utilise le proxy comme endpoint Docker, tandis que le socket brut `/var/run/docker.sock` existe sur l'hôte.
- Le service cible est démarré.
- Le fichier Compose et son éventuel fichier `.env` restent accessibles aux chemins indiqués par les labels Compose.
- L'image `docker:cli` peut être téléchargée avant l'interruption du proxy.

## Test 1 — disponibilité et garde-fous

1. Ouvrir **Updates** sur l'hôte `local`.
2. Vérifier que les conteneurs gérés par Compose, hors Dockman, proposent **Protected update**.
3. Vérifier que Dockman conserve son action de self-update dédiée.
4. Ouvrir un hôte distant : l'action protégée ne doit pas être proposée.
5. Essayer sur un conteneur Compose arrêté : le démarrage doit être refusé avec un message explicite.

Résultat attendu : aucune cible arbitraire, distante, arrêtée ou non-Compose ne peut utiliser ce mode.

## Test 2 — mise à jour de socketproxy

1. Publier ou sélectionner une nouvelle image de `socketproxy` compatible avec le Compose de test.
2. Dans **Updates**, cliquer **Protected update** sur `socketproxy`.
3. Lire l'avertissement puis confirmer.
4. Ne pas relancer Dockman pendant l'opération.
5. Attendre le retour de l'accès Docker, puis actualiser la vue.
6. Vérifier :
   - le conteneur `socketproxy` possède un nouvel ID et utilise la nouvelle image ;
   - Dockman fonctionne toujours ;
   - Monitor, Files et Updates accèdent de nouveau au daemon ;
   - `docker ps -a --filter name=dockman-protected-update` ne retourne aucun helper après succès.

Résultat attendu : la requête utilisateur reçoit immédiatement le démarrage de l'opération, le helper survit à la coupure du proxy, le service revient prêt et le helper s'auto-supprime.

## Test 3 — rollback automatique

1. Préparer une version volontairement défaillante du service : healthcheck `unhealthy`, processus qui s'arrête, ou configuration empêchant son démarrage.
2. Lancer **Protected update** et confirmer.
3. Attendre au maximum 90 secondes.
4. Vérifier que l'ancien service revient avec son image précédente et redevient opérationnel.
5. Examiner les traces :

   ```sh
   docker logs dockman-protected-update
   ```

6. Vérifier que le helper est conservé pour diagnostic après cet échec.
7. Corriger l'image puis relancer l'action : l'ancien helper arrêté doit être remplacé proprement.

Résultat attendu : l'échec ne laisse pas le proxy hors service ; l'image précédente est restaurée et vérifiée. Les traces expliquent l'étape ayant échoué.

## Test 4 — exclusion mutuelle

1. Lancer une mise à jour protégée dont la vérification dure plusieurs secondes.
2. Avant sa fin, tenter d'en lancer une seconde.

Résultat attendu : la seconde opération est refusée avec `another protected update is already in progress`. Le helper actif n'est ni arrêté ni remplacé.

## Test 5 — absence d'overhead

1. Après la fin du test, observer Dockman au repos pendant au moins cinq minutes.
2. Vérifier qu'aucun helper ne tourne après un succès.
3. Vérifier qu'aucun nouveau polling ou processus permanent n'apparaît.

Résultat attendu : ce mode ne consomme des ressources que lorsqu'il est déclenché manuellement.

## Récupération manuelle

En cas de double échec (mise à jour et rollback), conserver le helper et lire ses logs. L'ancien identifiant d'image et la référence restaurée y sont inscrits. Après correction manuelle :

```sh
docker rm -f dockman-protected-update
docker compose up -d --pull never --no-build --no-deps --force-recreate socketproxy
```

Adapter le nom du service au Compose concerné.
