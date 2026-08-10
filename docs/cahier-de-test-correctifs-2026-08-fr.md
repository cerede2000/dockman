# Cahier de test — correctifs 2026-08

Ce que la CI **ne peut pas** couvrir : systemd, tmpfs, vrai démon Docker,
registre distant, canal de notification réel. Le reste est vérifié
automatiquement et n'est pas repris ici.

Tout est mergé sur `integration`, image `:integration` disponible sur GHCR.

---

## Avant toute chose

### A. Le socket-proxy sort de la mise à jour automatique

C'est le changement de comportement le plus visible du lot.

1. Ouvrir la page des mises à jour, hôte par hôte.
2. Tout conteneur montant `/var/run/docker.sock` (socket-proxy, agents,
   Dockman lui-même) doit maintenant afficher **`protected`**, avec le motif
   « exposes the Docker socket… ».
3. Si l'un d'eux doit malgré tout être mis à jour automatiquement, poser
   `dockman.update=true` dessus : le label passe **avant** la protection, la
   ligne redevient éligible.

**Attendu** : aucun conteneur d'infrastructure enrôlé sans décision explicite.
**Régression à signaler** : un conteneur applicatif ordinaire passé en
`protected` — cela voudrait dire qu'il monte le socket sans que ce soit voulu.

### B. Ton builder Buildx `default`, s'il existe

Jusqu'ici, **chaque build** exécutait `docker rm --force buildx_buildkit_default`.
Si tu avais un builder `docker-container` nommé `default`, il était détruit
systématiquement.

```bash
docker buildx ls
```

**Attendu** : après un build depuis Dockman, ton builder est toujours là.

---

## 1. Mise à jour automatique

### 1.1 Un conteneur qui plante au démarrage est rollbacké

Le cas que le healthcheck ne détectait pas.

1. Prendre une stack de test sans `HEALTHCHECK` dans son image.
2. Lui donner une image volontairement cassée (par exemple un `command:` qui
   `exit 1` immédiatement), la tagger, l'enrôler avec rollback actif.
3. Lancer une exécution manuelle.

**Attendu** : l'exécution dure une dizaine de secondes, se termine en
`rolled_back`, l'ancien conteneur tourne à nouveau sous son nom d'origine, et
**aucun conteneur `*_updated` ne subsiste**.

```bash
docker ps -a --filter "name=_updated"
```

**Avant le correctif** : succès annoncé, ancien conteneur détruit.

### 1.2 Un conteneur avec HEALTHCHECK lent n'est pas condamné

1. Une image avec `HEALTHCHECK` et un `start_period` long (2 à 5 min).
2. Mise à jour forcée depuis l'interface.

**Attendu** : Dockman attend `start_period + 1 min` avant de conclure, et le
conteneur est accepté dès qu'il passe `healthy`. **Pas** d'échec prématuré.

### 1.3 Une panne réseau ne bloque plus définitivement

1. Enrôler un conteneur dont l'image vient d'un registre externe.
2. Couper le réseau sortant (ou pointer un registre injoignable).
3. Déclencher une exécution.

**Attendu** : résultat `skipped`, avec « will be retried ».
4. Rétablir le réseau, redéclencher.

**Attendu** : le conteneur est de nouveau tenté. **Avant le correctif** il
restait bloqué pour toujours après la première coupure.

### 1.4 Une durée d'exécution plus longue

Un conteneur sans `HEALTHCHECK` prend désormais **+10 s** par mise à jour
(fenêtre de stabilité). C'est attendu et borné. Une stack de dix services mis à
jour en transaction reste à dix fenêtres séquentielles au pire.

---

## 2. Secrets

### 2.1 Chiffrer puis déchiffrer une stack, runtime monté

Le critique. À faire sur une stack de test avec des secrets **fichier**.

1. Chiffrer la stack, attendre que le runtime volatil soit `ready`.
2. Vérifier que le tmpfs est bien monté :
   ```bash
   findmnt -rn -o FSTYPE,SOURCE --target /server/stacks/<stack>/.secrets
   ```
   → `tmpfs dockman-secrets`
3. Déchiffrer (« disable inline »).

**Attendu** : l'opération prend une à deux secondes de plus qu'avant (elle
attend le démontage), puis les secrets sont présents **en clair sur disque** :
```bash
findmnt -rn --target /server/stacks/<stack>/.secrets   # plus rien
ls -l /server/stacks/<stack>/.secrets/                  # les valeurs sont là
```
4. Redémarrer l'hôte, revérifier que les fichiers sont toujours là.

**Avant le correctif** : les secrets disparaissaient au démontage suivant.

### 2.2 Le refus quand le démontage n'arrive pas

1. Arrêter la réconciliation hôte :
   ```bash
   sudo systemctl stop dockman-secrets-reconcile.path
   ```
2. Tenter de déchiffrer une stack dont le tmpfs est monté.

**Attendu** : erreur explicite « still mounted… Nothing was changed », la stack
reste marquée chiffrée, `secrets.sops.yaml` intact, **aucun fichier en clair**
écrit dans `.secrets/`.
3. Relancer le `.path`, refaire l'opération : elle doit passer.

### 2.3 Une stack cassée ne prive plus les autres au boot

1. Corrompre volontairement le `secrets.sops.yaml` d'**une** stack chiffrée
   (couper le fichier en deux, par exemple).
2. Redémarrer l'hôte.

**Attendu** : les autres stacks chiffrées ont bien leurs secrets montés ; seule
la stack cassée est en échec.
```bash
systemctl status dockman-secrets-host.service   # échec signalé, avec le nom de la stack
findmnt -rn -o TARGET,SOURCE | grep dockman-secrets   # les autres sont montées
```
**Avant le correctif** : aucune stack n'était montée.

3. Ne pas oublier de restaurer le fichier.

### 2.4 La limite systemd n'est plus atteignable

1. Assigner un secret global à **au moins six** stacks chiffrées en une action.

**Attendu** : `dockman-secrets-reconcile.service` est déclenché **une seule
fois** :
```bash
journalctl -u dockman-secrets-reconcile.service --since "5 min ago" | grep Started | wc -l
```
→ `1`, et l'unité `.path` reste `active`.

**Avant le correctif** : six déclenchements, unité en échec, watch morte.

### 2.5 Réinstaller le runtime hôte

Le kit systemd a changé (limites de déclenchement). Regénérer et rejouer la
commande d'installation depuis l'onglet Secrets, puis :
```bash
systemctl cat dockman-secrets-reconcile.path | grep TriggerLimit
systemctl is-active dockman-secrets-reconcile.path
```

### 2.6 Nettoyage de la clé age sur installation distante

1. Lancer l'installation vers un hôte distant et **l'interrompre** (Ctrl-C)
   pendant l'étape `sudo … install`.
2. Sur l'hôte distant :
   ```bash
   ls -la /tmp/tmp.*/age-key.txt 2>/dev/null
   ```

**Attendu** : rien. **Avant le correctif** : l'identité age restait sur place.

---

## 3. Fuites

### 3.1 Un build échoué ne fait plus fuiter son log

Le test qui compte si tu as un canal de notification configuré.

1. Créer un `Dockerfile` qui échoue **après** avoir manipulé un jeton, par
   exemple :
   ```dockerfile
   FROM alpine
   RUN echo "//registry.npmjs.org/:_authToken=npm_FAKETOKEN123" > /tmp/.npmrc && cat /tmp/.npmrc && exit 1
   ```
2. Lancer le build depuis Dockman.

**Attendu** : la notification reçue contient l'hôte, l'image, le Dockerfile, le
statut et un renvoi vers Dockman — **et rien du log**. Le log complet reste
consultable dans l'interface.

**Avant le correctif** : le jeton partait dans le mail ou le webhook.

### 3.2 Une identité age n'est jamais poussée

1. Déposer un fichier d'identité age dans un dossier de stack synchronisé :
   ```bash
   cp /etc/dockman-secrets/age-key.txt /server/stacks/<stack>/age-key.txt
   ```
2. Ouvrir l'aperçu de synchronisation Git, **y compris** en cochant l'inclusion
   des fichiers sensibles avec la confirmation typée.

**Attendu** : le fichier apparaît en `age_identity` / ignoré, et n'est
transférable dans aucun cas.

3. Retirer le fichier.

**Avant le correctif** : il partait sur le dépôt comme du texte ordinaire, et
cette clé déchiffre tous les `secrets.sops.yaml` du fork.

---

## 4. Interface

### 4.1 Le sélecteur d'hôte

1. Ouvrir Réglages → Secrets **sans passer par une page d'hôte** (URL directe
   `/settings`).

**Attendu** : un sélecteur « Host » visible en haut à droite, indiquant l'hôte
ciblé, avec la liste des hôtes connectés. Changer d'hôte recharge le catalogue.

### 4.2 Les bandeaux ne mentent plus

1. Sur Réglages → Secrets, **sans stack sélectionnée**.

**Attendu** : « Runtime mode · select a stack » et « SOPS/age · select a stack »
en gris neutre, et le bandeau invite à choisir une stack.

**Avant le correctif** : « Migration mode · plaintext files » en orange — une
affirmation sur des secrets jamais consultés.

### 4.3 L'écrasement demande confirmation

1. Créer un secret global assigné à deux stacks.
2. Rouvrir « Apply / assign » sur ce même secret, garder les deux stacks,
   saisir une nouvelle valeur.

**Attendu** : un bandeau rouge nommant les stacks écrasées et un champ
`CONFIRM` obligatoire. Le bouton reste désactivé tant qu'il n'est pas saisi.
3. Assigner ce secret à une **nouvelle** stack seulement.

**Attendu** : aucune confirmation demandée — créer reste fluide, remplacer non.

### 4.4 L'échec de matérialisation est un avertissement

1. Arrêter `dockman-secrets-host.service`, chiffrer une stack avec des secrets
   fichier.

**Attendu** : un toast **orange** disant que les secrets sont chiffrés mais que
le runtime volatil n'est pas prêt. **Avant** : un toast vert.

---

## Non couvert par ces correctifs

- **§2.2 du plan** — la requête de réconciliation est écrite à la racine du
  système de fichiers de l'alias. Si tu utilises un alias **imbriqué** sous la
  racine de stacks configurée (`StackRoot=/server/stacks` + alias
  `media` → `/server/stacks/media`), la réconciliation automatique de ces
  stacks ne partira pas. Contournement : `sudo systemctl start
  dockman-secrets-host.service`. Corriger proprement demande de mémoriser la
  racine de stacks par hôte côté Dockman — conception, pas correction.
- Un `buildx_buildkit_default` laissé par une très vieille version de Dockman
  n'est plus supprimé automatiquement : `docker rm -f buildx_buildkit_default`
  une fois, à la main, si tu en as un et qu'il ne t'appartient pas.
