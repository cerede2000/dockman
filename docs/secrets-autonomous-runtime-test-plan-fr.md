# Secrets — cahier de test du runtime autonome chiffré

## 1. Préparation et clé obligatoire

1. Générer la clé avec `docker exec dockman dockman-age-keygen`.
2. Sauvegarder le fichier privé hors du serveur et relever le recipient public.
3. Configurer `DOCKMAN_SOPS_AGE_KEY_FILE` et
   `DOCKMAN_SOPS_AGE_RECIPIENT`, puis recréer Dockman.
4. Sélectionner une stack dans **Settings → Secrets**.

Attendu : SOPS/age passe à **ready** seulement après sélection de la stack. Une
clé absente, trop permissive ou ne correspondant pas au recipient interdit
l'activation et ne supprime aucun fichier existant.

## 2. Installation autonome sur l'hôte

1. Monter les stacks dans Dockman avec la même source et destination absolue et
   la propagation `rslave`, par exemple :

   ```yaml
   volumes:
     - /server/stacks:/server/stacks:rslave
   ```

2. Ouvrir **Host boot wizard**, vérifier le nom du conteneur, le chemin des
   stacks et les deux chemins de clé.
3. Copier puis exécuter la commande sur l'hôte.
4. Vérifier :

   ```console
   sudo systemctl is-enabled dockman-secrets-host.service
   sudo systemctl is-enabled dockman-secrets-reconcile.path
   sudo systemctl is-active dockman-secrets-reconcile.path
   sudo systemctl status dockman-secrets-host.service
   sudo stat -c '%a %U:%G %n' /etc/dockman-secrets-host.json \
     /etc/dockman-secrets/age-key.txt
   ```

Attendu : service de boot et path de réconciliation activés, fichiers sensibles
en `0600`, aucun secret affiché par la commande ou journalisé. Le helper est un
`oneshot` terminé, sans daemon ni boucle de polling.

## 3. Migration sans plaintext persistant

1. Utiliser `FILE_TOKEN` en fichier et `INLINE_TOKEN` en environnement :

   ```yaml
   secrets:
     file_token:
       file: ./.secrets/FILE_TOKEN

   services:
     test:
       image: alpine:3.22
       read_only: true
       environment:
         INLINE_TOKEN: ${INLINE_TOKEN}
       secrets:
         - file_token
       command: ["sh", "-c", "test -s /run/secrets/file_token && sleep infinity"]
   ```

2. Cliquer **Initialize encrypted runtime** et saisir `CONFIRM` sans créer de
   valeur factice.
3. Depuis **Global secret assignments**, affecter `FILE_TOKEN` et
   `INLINE_TOKEN` à la stack.
4. Vérifier sans redémarrer manuellement de service :

   ```console
   findmnt /server/stacks/chemin/stack/.secrets
   sudo journalctl -u dockman-secrets-reconcile.service -n 20
   ```

Attendu : `secrets.sops.yaml`, `.dockman-sops-inline` et `compose-sops.sh`
existent ; la valeur n'apparaît dans aucun de ces fichiers. `.secrets` est de
type `tmpfs`. La création de la stack et de ses secrets a déclenché le oneshot
automatiquement et le service `read_only` démarre avec son secret fichier.

## 4. Cloisonnement Docker

1. Vérifier que seul le conteneur déclarant `file_token` possède
   `/run/secrets/file_token`.
2. Vérifier qu'un autre conteneur de la stack ne peut pas le lire.
3. Exécuter `docker inspect`.

Attendu : `FILE_TOKEN` n'est pas dans l'inspect ; `INLINE_TOKEN` y apparaît,
ce qui est la limite explicite du mode environnement Docker. Aucun répertoire
global de secrets n'est monté dans les workloads.

## 5. Reboot complet sans Dockman

1. Stopper Dockman et redémarrer l'hôte.
2. Vérifier l'ordre :

   ```console
   systemd-analyze critical-chain docker.service
   sudo journalctl -u dockman-secrets-host.service -b
   ```

3. Vérifier que les conteneurs `restart: unless-stopped` reviennent seuls et
   lisent leur secret fichier.

Attendu : le runtime Secrets termine avant Docker. Aucun appel à l'API Dockman
n'est nécessaire et le plaintext n'existe qu'en tmpfs.

## 6. Recovery manuel sans Dockman

1. Laisser Dockman arrêté.
2. Exécuter :

   ```console
   sudo systemctl restart dockman-secrets-host.service
   cd /server/stacks/chemin/stack
   SOPS_AGE_KEY_FILE=/etc/dockman-secrets/age-key.txt ./compose-sops.sh config
   SOPS_AGE_KEY_FILE=/etc/dockman-secrets/age-key.txt ./compose-sops.sh up
   ```

Attendu : file et inline fonctionnent. Une clé incorrecte bloque avant Compose.
Si le tmpfs fichier manque, le script donne la commande systemd à exécuter.

## 7. Rotation, suppression et Git

1. Modifier un secret depuis Dockman et vérifier que le ciphertext change.
2. Vérifier que le tmpfs est actualisé automatiquement, puis recréer uniquement
   le consommateur.
3. Supprimer un secret après avoir retiré sa référence Compose.
4. Synchroniser la stack vers Git.

Attendu : Git reçoit le ciphertext et les fichiers portables, jamais
`.secrets` ni `.dockman-secrets-reconcile`. Une valeur supprimée disparaît du
tmpfs au cours de la réconciliation explicite déclenchée par Dockman.

## 8. Garde-fous

- placer un fichier persistant dans `.secrets` avant le montage : le helper
  refuse de le masquer ;
- remplacer le marqueur ou la source par un symlink : refus ;
- modifier le marqueur en `version=99` : refus ;
- altérer le MAC SOPS : aucune valeur n'est remplacée ;
- ouvrir la clé en `0644` : refus avec demande de `chmod 0600` ;
- dépasser 1 MiB par valeur ou 4 MiB de source : refus borné ;
- vérifier après dix minutes au repos : aucun processus SOPS/helper, aucun log
  répétitif et aucun overhead CPU/RAM ajouté.

## 9. Multi-hôtes

Répéter les sections 2, 5 et 6 sur chaque hôte. Avec une instance Dockman
centrale, installer la clé correspondant au recipient configuré sur chaque
hôte. Pour une séparation cryptographique stricte, utiliser une instance et un
recipient distincts par périmètre d'hôtes.
