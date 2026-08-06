# Secrets — cahier de test du lot 5 SOPS inline

## 1. Préparation locale sans Git

1. Utiliser une stack locale sans folder link Git.
2. Configurer `DOCKMAN_SOPS_AGE_KEY_FILE` et `DOCKMAN_SOPS_AGE_RECIPIENT`.
3. Créer dans **Settings → Secrets** deux valeurs de test : `API_TOKEN` et
   `DATABASE_PASSWORD`.
4. Les référencer avec `${API_TOKEN}` et `${DATABASE_PASSWORD}` dans le
   `compose.yml` de test.

Attendu : la stack fonctionne dans le mode fichier initial et aucune valeur
n'apparaît dans l'API, les logs ou `secrets.sops.yaml`.

## 2. Activation inline

1. Cliquer sur **Enable inline**, vérifier le manifeste proposé et saisir
   `CONFIRM`.
2. Vérifier dans la stack :

```console
test ! -e .secrets
test -f secrets.sops.yaml
test -f .dockman-sops-inline
test -x compose-sops.sh
grep -F 'API_TOKEN' secrets.sops.yaml
! grep -F 'la-valeur-secrete' secrets.sops.yaml
```

Attendu : `.secrets`, y compris son historique, a disparu seulement après la
vérification du ciphertext. Le mode affiché est **Encrypted inline · active**.

## 3. Cycle automatique local

1. Modifier `API_TOKEN` depuis Dockman.
2. Recharger la page et révéler explicitement la valeur.
3. Créer `NEW_VALUE`, puis la supprimer.
4. Vérifier après chaque action que `.secrets` ne réapparaît jamais.
5. Lancer successivement validation, Up, Restart, Stop et Start.

Attendu : chaque écriture remplace atomiquement le ciphertext, les actions
Compose reçoivent les valeurs et aucune opération Git n'est nécessaire.

## 4. Absence d'overhead au repos

1. Laisser la page et la stack inactives plusieurs minutes.
2. Contrôler les processus, logs, CPU et mémoire de Dockman.

Attendu : aucun processus SOPS résident, aucun polling supplémentaire, aucun
log répétitif et aucune hausse de l'overhead au repos.

## 5. Recovery indépendant

1. Depuis un shell disposant de Docker Compose et SOPS, définir :

```console
export SOPS_AGE_KEY_FILE=/chemin/securise/dockman-sops-age-key.txt
```

2. Sans utiliser l'interface ou l'API Dockman, exécuter :

```console
./compose-sops.sh config
./compose-sops.sh up
./compose-sops.sh ps
```

Attendu : la stack est validée et démarrée. Une clé absente ou incorrecte fait
échouer l'action avant Compose, avec un message SOPS explicite.

## 6. Hôte SSH

1. Activer inline sur une stack de test d'un hôte SSH.
2. Exécuter validation et Up depuis Dockman.
3. Vérifier sur l'hôte distant qu'aucun fichier secret temporaire n'est créé et
   que la ligne de commande Compose ne contient pas la valeur.

Attendu : la valeur transite uniquement dans le canal SSH chiffré sur stdin.
Pour un recovery autonome directement sur l'hôte distant, restaurer séparément
SOPS et la clé age avant d'utiliser `compose-sops.sh`.

## 7. Retour au mode fichier

1. Cliquer sur **Materialize and disable** et saisir `CONFIRM`.
2. Vérifier les valeurs sous `.secrets`, puis recréer la stack.

Attendu : les valeurs sont matérialisées avant la suppression du marqueur et du
script. `secrets.sops.yaml` est conservé comme source de récupération.

## 8. Garde-fous

- tenter un nom `token-with-dash` en inline : refus ;
- tenter une valeur supérieure à 1 MiB : refus ;
- altérer le MAC SOPS : aucune action Compose ne démarre ;
- supprimer la clé age : le mode reste identifié inline mais est indisponible ;
- vérifier qu'un `docker compose up` direct sans wrapper ne reçoit pas les
  variables, tandis que `./compose-sops.sh up` les reçoit.
