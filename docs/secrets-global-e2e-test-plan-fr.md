# Secrets — environnement E2E propre, réconciliation automatique et catalogue global

Ce scénario repart de zéro sans réutiliser la stack ni les valeurs du premier
test. Les deux stacks sont réservées à la validation et les valeurs proposées
ne sont pas des secrets de production.

## 1. Arborescence neuve

Copier les deux stacks livrées dans `testdata/secrets-e2e`, ou les créer depuis
Files avec le contenu ci-dessous :

```text
/server/stacks/compose/dockman-secrets-lab-a/compose.yml
/server/stacks/compose/dockman-secrets-lab-b/compose.yml
```

Lab A valide fichier, inline, rootfs read-only et cloisonnement :

```yaml
name: dockman-secrets-lab-a
services:
  file-reader:
    image: alpine:3.22
    user: "65534:65534"
    read_only: true
    network_mode: none
    restart: unless-stopped
    command: ["sh", "-ec", "test -s /run/secrets/shared_token; echo FILE_READY; exec sleep infinity"]
    secrets:
      - source: shared_token
        target: shared_token
        mode: 0444
  inline-reader:
    image: alpine:3.22
    read_only: true
    network_mode: none
    restart: unless-stopped
    environment:
      INLINE_TOKEN: ${INLINE_TOKEN:?INLINE_TOKEN is required}
    command: ["sh", "-ec", "test -n \"$$INLINE_TOKEN\"; echo INLINE_READY; exec sleep infinity"]
  isolated:
    image: alpine:3.22
    read_only: true
    network_mode: none
    restart: unless-stopped
    command: ["sh", "-ec", "test ! -e /run/secrets/shared_token; test -z \"$${INLINE_TOKEN:-}\"; echo ISOLATED; exec sleep infinity"]
secrets:
  shared_token:
    file: ./.secrets/SHARED_TOKEN
```

Lab B valide l'affectation globale indépendante :

```yaml
name: dockman-secrets-lab-b
services:
  consumer:
    image: alpine:3.22
    user: "65534:65534"
    read_only: true
    network_mode: none
    restart: unless-stopped
    command: ["sh", "-ec", "test -s /run/secrets/shared_token; echo SECOND_STACK_READY; exec sleep infinity"]
    secrets:
      - source: shared_token
        target: shared_token
        mode: 0444
secrets:
  shared_token:
    file: ./.secrets/SHARED_TOKEN
```

Ne créer aucun secret et ne déployer aucune stack à ce stade.

## 2. Installation hôte propre

1. Recréer Dockman avec `/server/stacks:/server/stacks:rslave`.
2. Dans Settings → Secrets, ouvrir Host boot wizard et exécuter la commande.
3. Vérifier :

```console
systemctl is-enabled dockman-secrets-host.service
systemctl is-enabled dockman-secrets-reconcile.path
systemctl is-active dockman-secrets-reconcile.path
```

Attendu : les deux unités sont activées et le path est actif. Le helper
principal est un oneshot sans processus résident.

## 3. Initialisation sans plaintext

Pour chacune des deux stacks :

1. la charger dans Settings → Secrets ;
2. vérifier SOPS/age ready ;
3. cliquer Initialize encrypted runtime ;
4. saisir `CONFIRM`.

Ne créer aucune valeur factice. Attendu : source SOPS, marqueur et script de
recovery présents, `.secrets` monté automatiquement en tmpfs sans commande
systemctl manuelle. Le catalogue global affiche les deux stacks en mode
encrypted.

## 4. Affectations globales

Dans Global secret assignments :

1. créer `SHARED_TOKEN` avec `lab-shared-2026-v1` ;
2. sélectionner Lab A et Lab B ;
3. valider Encrypt and apply ;
4. créer `INLINE_TOKEN` avec `lab-inline-2026-v1` ;
5. sélectionner seulement Lab A.

Attendu : SHARED_TOKEN montre deux affectations, INLINE_TOKEN une seule. Chaque
stack possède son propre ciphertext et aucune valeur n'est dans SQLite ou dans
un fichier persistant en clair.

## 5. Déploiement et contrôles

Lancer Up pour chaque stack depuis Dockman. Vérifier les quatre conteneurs et
leurs logs `FILE_READY`, `INLINE_READY`, `ISOLATED` et `SECOND_STACK_READY`.
Contrôler que SHARED_TOKEN est absent de Docker inspect, qu'INLINE_TOKEN est
présent uniquement dans l'environnement du consommateur inline et qu'aucun
secret n'est visible dans le conteneur isolated.

## 6. Rotation globale

Réappliquer SHARED_TOKEN avec `lab-shared-2026-v2` sur les deux stacks, puis
faire Down/Up des deux stacks. Vérifier que les deux empreintes SHA-256 du
fichier concordent et que l'ancienne valeur n'est présente dans aucun fichier
persistant.

## 7. Git

Lier les deux dossiers à un dépôt de test. Vérifier que compose.yml,
secrets.sops.yaml, `.dockman-sops-inline` et compose-sops.sh sont transférés,
mais jamais `.secrets`, la clé age ou `.dockman-secrets-reconcile`.

## 8. Reboot et indépendance

Redémarrer l'hôte sans intervention préalable. Vérifier dans le journal du boot
que dockman-secrets-host termine avant Docker, que les deux tmpfs existent et
que les quatre conteneurs reviennent. Arrêter Dockman, puis exécuter dans chaque
stack `SOPS_AGE_KEY_FILE=/etc/dockman-secrets/age-key.txt ./compose-sops.sh
down` et `up`. Tout doit fonctionner sans API Dockman.

## 9. Nettoyage explicite

Après validation, faire Down sur les deux stacks, retirer leurs folder links de
test, puis supprimer uniquement :

```text
/server/stacks/compose/dockman-secrets-lab-a
/server/stacks/compose/dockman-secrets-lab-b
```

Déclencher une dernière réconciliation avec
`sudo systemctl start dockman-secrets-reconcile.service` afin que le helper
démonte les tmpfs devenus orphelins.
