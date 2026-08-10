# Cahier de validation — secrets Dockman

Objectif : prouver, sur une infrastructure réelle, que le modèle de secrets tient ses
quatre promesses.

1. Rien en clair sur le disque persistant en mode chiffré.
2. Un secret fichier n'atteint que les services qui le déclarent.
3. Après reboot de l'hôte, tout remonte **automatiquement**, avant Docker.
4. Une stack chiffrée démarre **sans Dockman**, avec seulement Docker et SOPS.

Prérequis : image `ghcr.io/cerede2000/dockman:integration` à jour, kit systemd
réinstallé depuis **Réglages → Secrets → Host boot wizard** (les correctifs systemd
et le script de récupération n'existent que dans le kit régénéré).

Notation : `✅` attendu, `❌` échec à me signaler.

---

## 1. Préparation — deux stacks de test

Crée deux stacks dans ta racine Dockman. La seconde existe pour prouver qu'elle
**n'a pas** accès aux secrets de la première.

### `secrets-lab/compose.yml`

```yaml
services:
  # A : consomme un secret FICHIER. Le seul à le déclarer.
  reader-file:
    image: alpine:3.20
    command: >
      sh -c "echo '--- reader-file ---';
             echo -n 'db.password = '; cat /run/secrets/db.password;
             echo 'API_TOKEN vu dans env = '${API_TOKEN:-<absent>};
             sleep infinity"
    secrets:
      - db.password

  # B : consomme un secret INLINE (variable d'environnement).
  reader-inline:
    image: alpine:3.20
    environment:
      APP_TOKEN: ${API_TOKEN}
    command: >
      sh -c "echo '--- reader-inline ---';
             echo 'APP_TOKEN = '$$APP_TOKEN;
             echo -n 'db.password lisible ? '; cat /run/secrets/db.password 2>/dev/null || echo NON;
             sleep infinity"

  # C : ne déclare RIEN. Ne doit voir aucun secret fichier.
  no-access:
    image: alpine:3.20
    command: >
      sh -c "echo '--- no-access ---';
             echo -n 'db.password lisible ? '; cat /run/secrets/db.password 2>/dev/null || echo NON;
             echo -n 'contenu de /run/secrets: '; ls -A /run/secrets 2>/dev/null || echo '<vide>';
             sleep infinity"

secrets:
  db.password:
    file: ./.secrets/db.password
```

### `secrets-outsider/compose.yml`

Stack **non chiffrée**, qui ne doit accéder à rien.

```yaml
services:
  outsider:
    image: alpine:3.20
    command: >
      sh -c "echo '--- outsider ---';
             echo -n 'API_TOKEN = '; echo ${API_TOKEN:-<absent>};
             echo -n 'db.password lisible ? '; cat /run/secrets/db.password 2>/dev/null || echo NON;
             sleep infinity"
```

> **Note sur le nommage — c'est le cœur du test.** `db.password` contient un point :
> il n'est **jamais** exporté dans l'environnement Compose, il n'existe que comme
> fichier. `API_TOKEN` est un nom de variable valide : il est exporté à **toute la
> stack**. Les deux comportements sont voulus, et le tableau final le vérifie.

---

## 2. Actions dans Dockman

1. **Réglages → Secrets**, sélectionne ton hôte dans le sélecteur en haut à droite.
2. Choisis la stack `secrets-lab`.
3. **New secret** → nom `API_TOKEN`, valeur `token-inline-42`.
   ✅ Le champ indique « Exported to the Compose environment ».
4. **New secret** → nom `db.password`, valeur `motdepasse-fichier-99`.
   ✅ Le champ indique « File only ».
5. Vérifie la colonne **Scope** du tableau :
   ✅ `API_TOKEN` → `environment · whole stack` (orange)
   ✅ `db.password` → `file · declared services` (vert)
6. **Encrypt / enable encrypted runtime** sur `secrets-lab`, confirmation typée.
   ✅ Toast **vert** annonçant le runtime volatil prêt.
   ❌ Un toast **orange** signifie que le runtime hôte n'a pas monté le tmpfs —
   arrête-toi et vérifie `systemctl status dockman-secrets-host`.
7. Déploie `secrets-lab` puis `secrets-outsider`.

---

## 3. Validation du cloisonnement

```bash
docker compose -f secrets-lab/compose.yml logs reader-file reader-inline no-access
docker compose -f secrets-outsider/compose.yml logs outsider
```

| Service | `db.password` (fichier) | `API_TOKEN` (env) | Attendu |
|---|---|---|---|
| `reader-file` | **lisible** | absent | ✅ le déclare |
| `reader-inline` | **NON** | `token-inline-42` | ✅ inline = toute la stack |
| `no-access` | **NON**, `/run/secrets` vide | absent | ✅ ne déclare rien |
| `outsider` | **NON** | `<absent>` | ✅ autre stack |

**Le point à retenir** : `reader-inline` ne lit pas `db.password` alors qu'il est dans
la même stack. C'est la démonstration que le secret fichier est cloisonné **par
service**, là où l'inline ne l'est que **par stack**.

---

## 4. Validation du chiffrement au repos

```bash
cd <racine>/secrets-lab

# Le ciphertext est le seul stockage persistant
cat secrets.sops.yaml            # ✅ ENC[AES256_GCM,...] partout
grep -c "motdepasse-fichier-99" secrets.sops.yaml   # ✅ 0
grep -c "token-inline-42" secrets.sops.yaml         # ✅ 0

# Le clair vit en RAM
findmnt -rn -o FSTYPE,SOURCE,TARGET --target .secrets
# ✅ tmpfs dockman-secrets <chemin>/.secrets

ls -l .secrets/                  # ✅ db.password ET API_TOKEN, mode 0400
```

> Les **deux** valeurs sont matérialisées en fichiers ; seul `API_TOKEN` est **en
> plus** exporté dans l'environnement. Le nom ne décide pas de l'existence du
> fichier, il décide de l'export.

**Preuve que rien n'est sur le disque persistant :**

```bash
sudo umount .secrets && ls -A .secrets/   # ✅ vide
sudo systemctl start dockman-secrets-reconcile.service
ls -A .secrets/                            # ✅ les fichiers sont revenus
```

---

## 5. Validation du reboot — la promesse la plus importante

```bash
sudo reboot
```

Au retour, **sans aucune action** :

```bash
systemctl status dockman-secrets-host.service     # ✅ active (exited)
systemctl is-active docker                        # ✅ active
findmnt -rn -o SOURCE --target <racine>/secrets-lab/.secrets   # ✅ dockman-secrets
docker compose -f secrets-lab/compose.yml logs reader-file     # ✅ le mot de passe
```

Vérifie l'ordre — c'est le correctif du cycle systemd :

```bash
systemctl list-dependencies --before dockman-secrets-host.service | grep docker
journalctl -b -u dockman-secrets-host.service
```

✅ `dockman-secrets-host` s'est exécuté **avant** `docker.service`.
❌ Si Docker n'a pas démarré seul, c'est le cycle systemd — signale-le-moi.

---

## 6. Validation de l'indépendance — le cahier des charges

**À faire sur une VM neuve**, avec uniquement Docker, SOPS et ta clé age.

```bash
# Sur la VM neuve, après avoir copié le dossier secrets-lab :
export SOPS_AGE_KEY_FILE=/chemin/vers/age-key.txt
cd secrets-lab
sudo ./compose-sops.sh up
```

✅ La stack démarre. Le script installe rien, n'appelle aucun `systemctl`, monte un
tmpfs lui-même et déchiffre les secrets fichier par extraction unitaire.

```bash
findmnt -rn -o SOURCE --target .secrets   # ✅ tmpfs (car lancé en root)
docker compose logs reader-file            # ✅ le mot de passe
grep -c systemctl compose-sops.sh          # ✅ 0
sudo ./compose-sops.sh secrets-clean       # nettoyage
```

**Sans root**, le script doit continuer en avertissant :

```bash
./compose-sops.sh up
# ✅ "warning: not running as root; secrets will be written to disk in .secrets"
# ✅ la stack démarre quand même
```

> C'est un arbitrage que j'ai posé : une stack qui ne démarre pas est pire qu'un
> avertissement sur du clair en disque. Si tu préfères l'échec, dis-le-moi.

---

## 7. Validation du déchiffrement — le correctif critique

Le bug corrigé : `DisableInline` matérialisait **avant** de libérer le tmpfs, donc
écrivait le clair dans la mémoire qu'il s'apprêtait à jeter.

```bash
findmnt -rn -o SOURCE --target .secrets   # tmpfs monté avant l'opération
```

Dans Dockman : **Réglages → Secrets → secrets-lab → Disable encrypted runtime**.

```bash
findmnt -rn --target .secrets              # ✅ plus rien
ls -l .secrets/                            # ✅ les fichiers sont là, EN CLAIR
cat .secrets/db.password                   # ✅ motdepasse-fichier-99
ls .dockman-sops-inline 2>/dev/null        # ✅ absent
ls secrets.sops.yaml                       # ✅ conservé
sudo reboot                                # ✅ après reboot, toujours là
```

❌ Des fichiers vides ou absents = le critique n'est pas corrigé.

**Le refus fail-closed :**

```bash
# Rechiffre d'abord, puis casse la réconciliation :
sudo systemctl stop dockman-secrets-reconcile.path
```

Tente à nouveau **Disable** dans Dockman :

✅ Erreur explicite « still mounted… Nothing was changed »
✅ `.secrets/` ne contient **aucun** fichier en clair
✅ `.dockman-sops-inline` toujours présent

```bash
sudo systemctl start dockman-secrets-reconcile.path   # remets en route
```

---

## 8. Pièges à vérifier au passage

**Une réinstallation ne doit plus arracher les secrets.** Stack chiffrée démarrée,
relance le Host boot wizard :

```bash
findmnt -rn --target <racine>/secrets-lab/.secrets   # ✅ toujours monté pendant et après
```

❌ S'il disparaît, l'activation utilise encore `restart`.

**Le conteneur Dockman est protégé.** Page **Updates** :

✅ Ton socket-proxy affiche `Protected (socket)` **et** garde son bouton
« Protected update ».
✅ Dockman lui-même affiche `Protected`.
✅ Depuis le **Monitor**, Update sur le conteneur Dockman renvoie une erreur
explicite au lieu de l'arrêter.

**Une identité age ne part jamais sur Git.**

```bash
cp /etc/dockman-secrets/age-key.txt <racine>/secrets-lab/age-key.txt
```

Ouvre l'aperçu de synchronisation Git, **coche l'inclusion des fichiers sensibles**
et saisis la confirmation :

✅ `age-key.txt` apparaît ignoré (`age_identity`), non transférable même ainsi.

```bash
rm <racine>/secrets-lab/age-key.txt
```

---

## 9. Nettoyage

```bash
docker compose -f secrets-lab/compose.yml down
docker compose -f secrets-outsider/compose.yml down
```

Puis supprime les deux stacks depuis Dockman.

---

## Ce que ce cahier ne couvre pas

- **§2.2** — si tu utilises un alias **imbriqué** sous ta racine de stacks
  (`StackRoot=/server/stacks` + alias `media` → `/server/stacks/media`), la
  réconciliation automatique de ces stacks ne part pas. Contournement :
  `sudo systemctl start dockman-secrets-host.service`. Non corrigé.
- L'inline reste visible via `docker inspect` et `/proc/<pid>/environ` pour qui a un
  accès root à l'hôte. Le chiffrement protège le **repos**, pas une machine allumée
  dont on a le contrôle.
