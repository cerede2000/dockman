# Revue de code complète — fork Dockman

**Date** : 28 juillet 2026
**Périmètre** : `hardening/dependencies-git-preview-performance` @ `700e801` (= `integration` @ `f67cf8c` + le lot dépendances/performances)
**Méthode** : revue par quatre passes parallèles (gitsync backend, backend Docker/sécurité, interface React, infrastructure CI/branches), puis **revalidation individuelle de chaque finding sur le code courant**. Tout ce qui figure ici a été vérifié dans le code à la date ci-dessus ; les points déjà corrigés entre-temps ont été retirés.

---

## 0. Synthèse

| Sévérité | Nombre | Domaines |
|---|---|---|
| Critique | 0 | — |
| Majeur | 10 | gitsync (3), backend Docker (4), interface (3) |
| Mineur | ~25 | tous domaines |

**Aucun chemin de perte de données silencieuse n'a été trouvé** dans la synchronisation git : les couches baseline/conflit, les sauvegardes obligatoires avant écriture, la préservation des suppressions et les confirmations textuelles se recoupent correctement. Le vault de credentials, la validation anti-traversée de chemins et le refus des symlinks sont solides.

Les problèmes se concentrent ailleurs : **absence totale de timeouts réseau** dans gitsync, **un chemin destructif dans l'updater de conteneurs**, **deux contournements du drapeau `ALLOW_SELF_EXEC`**, et côté interface **quatre composants majeurs que le React Compiler abandonne silencieusement**.

### Déjà corrigé (pour mémoire, ne pas retraiter)

- Entrée `GO-2026-5617` retirée de l'allowlist govulncheck → job Go débloqué.
- `react-router` migré en v8 (vulnérabilité HIGH de production) et `brace-expansion` remonté en 5.0.8 → `npm audit` débloqué, y compris la cascade electron-builder.
- Confirmation de suppression sur l'action groupée de la barre d'outils Monitor (champ `confirm` générique dans `ActionButtons`).

### Ordre de traitement recommandé

1. `defer close(done)` dans le runner SSH — une ligne, débloque toute annulation de commande distante.
2. Littéral BigInt de `LogRow` — une ligne, gain direct sur le composant le plus chaud.
3. Chemin destructif de `ContainerRecreate` — risque de perte définitive de conteneur.
4. Timeouts réseau gitsync — un gel réseau arrête toute la synchronisation automatique.
5. Purge de sélection sur la recherche Monitor — action destructive sur des lignes invisibles.
6. `try/finally` de MonitorPage — refactor mécanique, débloque la mémoïsation de toute la vue.
7. Route de récupération d'un dépôt « diverged ».

---

## 1. Synchronisation Git (`core/internal/gitsync`, ~16 000 lignes)

### 1.1 Architecture

Un **dépôt** est un clone go-git « compact » : objets seuls, sans copie de travail, sous `workspaceRoot/<uuid>`, marqué `dockman-object-store-v1`.

Un **binding** relie `hôte + chemin de stack` à un sous-chemin du dépôt. Il porte un catalogue de fichiers Compose, une sélection, un profil de synchronisation (`compose_only`, `compose_config`, `all_files`) et des motifs d'inclusion/exclusion.

La **baseline** (table `git_binding_baselines`, SHA-256 par fichier) est la vérité de comparaison. `buildPreview` classe chaque fichier :

- **add** / **modify** — transfert possible ;
- **conflict** — `no_baseline` (jamais transféré) ou `destination_changed` (modifié des deux côtés) ;
- **preserved** — supprimé côté Git, conservé localement ;
- **deleted_locally** — supprimé localement, jamais propagé automatiquement.

Chaque transfert exige un **jeton de preview** (hash SHA-256 des entrées) revalidé sous verrou, et le SHA de chaque fichier est re-vérifié pendant la copie (`streamTransferFile`).

**Synchronisation automatique** : une goroutine unique se réveille toutes les 1 à 30 secondes et traite séquentiellement les bindings dus (intervalle configurable de 5 minutes à 24 heures). Chaque exécution enchaîne : fetch → pull *fast-forward only* → preview → import avec sauvegarde `pre_import` (archive tar.gz + manifeste avant/après, confinée par `os.OpenRoot`) → déploiement contrôlé optionnel (provisioning → validation → dry-run → up, avec rollback santé restaurant les fichiers depuis la sauvegarde).

**Dockman → Git** (`ExportBinding`) : fetch, vérification d'état propre, écriture des fichiers sélectionnés dans un checkout temporaire partageant le store d'objets, commit, push, puis mise à jour de la baseline.

**Git → Dockman** (`ImportBinding`) : refus d'écraser tout fichier divergent de la baseline, exclusion des stacks dont l'éditeur est « sale » (`excludeDirtyEditorStacks`), exclusion des Compose invalides, sauvegarde systématique avant écriture.

Les décisions destructrices (orphelins, suppressions locales à propager vers Git) exigent une confirmation textuelle et mettent l'automatisation en pause « récupération ».

### 1.2 Ce qui est sain (vérifié)

- **Vault / credentials** : AES-256-GCM, nonce aléatoire, données associées liées à l'UUID du credential, clé maître en `0600` dans un dossier `0700`, purge `Unscoped` à la suppression. Aucun secret dans les vues API (`SecretHint` = empreinte publique SSH) ni dans les logs. Les URL sont normalisées avec refus de tout *userinfo*. Les clés d'hôte SSH GitHub sont épinglées via l'API.
- **Traversée de chemins / injection** : aucune commande shell (go-git pur). `validateRelativePath` est appliqué partout, y compris sur les entrées d'arbre Git et sur les entrées d'archives tar. `.git` est interdit, les symlinks systématiquement refusés, les écritures confinées par `os.OpenRoot`.
- **Git → Dockman** : un pull ne peut pas écraser une modification locale non commitée ; les suppressions distantes ne sont jamais appliquées automatiquement ; sauvegarde avant chaque écriture ; garde éditeur opérationnelle.
- **Cas limites** : dépôt et branche vides gérés (`createEmptyRemoteBranch`), fichiers > 100 Mio et dossiers > 2 000 fichiers auto-exclus sans lecture, détection de binaire pour la comparaison, budget global 20 000 fichiers / 2 Gio. Environ 150 tests couvrent conflits, orphelins, provisioning, limites et traversée.

### 1.3 [MAJEUR] Aucun timeout réseau — un fetch suspendu gèle toute l'automatisation

**Fichiers** : `core/internal/gitsync/automation.go:188-201` et `232-250`, `core/internal/gitsync/repository.go:483`

La goroutine d'automatisation et sa boucle séquentielle appellent `FetchRepository` → `repo.FetchContext` **avec le contexte du serveur, sans échéance**. Idem pour tous les `PushContext` et `ListContext`. Vérification : `grep -c "context.WithTimeout" core/internal/gitsync/*.go` retourne **0**. Le client HTTP à 20 secondes de `service.go:116-121` ne couvre que l'API GitHub, pas le transport go-git.

**Scénario** : une connexion TCP à moitié morte — banal en homelab (VPN qui tombe, NAT qui expire, dépôt auto-hébergé qui redémarre) — bloque l'opération indéfiniment. Comme la goroutine est unique et le traitement séquentiel, **plus aucun binding n'est synchronisé**. Les verrous `automation:<id>` et dépôt restent tenus, donc toute action manuelle répond « déjà en cours ». Seul un redémarrage de Dockman rétablit la situation.

**Correction** : encadrer chaque opération réseau d'un `context.WithTimeout` (2 à 5 minutes) dans `runDueAutoSyncs` et `fetchRepositoryLocked`.

### 1.4 [MAJEUR] Un push rejeté condamne le dépôt, sans route de récupération

**Fichiers** : `core/internal/gitsync/binding.go:1033-1041`, `local_deletion.go:291-301` et `460-470`, `repository.go:578-580` et `620-622`, `handler_http.go:20-71`

`ExportBinding` commite sur la branche partagée **puis** pousse. Si un écrivain externe pousse entre le fetch et le push, le push échoue mais **le commit local subsiste** : le dépôt passe en état « diverged ». À partir de là :

- le pull est refusé — « local and remote history require an explicit conflict decision » ;
- le push est refusé — « remote contains commits that are not present locally » ;
- l'automatisation se met en `blocked`.

Aucune route de réinitialisation n'existe (vérifié : aucune occurrence de `reset`, `force-push` ou `discard` dans `handler_http.go`), et `DeleteRepository` est refusé tant que des bindings existent. La seule sortie est de tout délier, supprimer le dépôt, le recréer — et **perdre toutes les baselines**, ce qui repasse chaque fichier en conflit `no_baseline`.

**Correction** : ajouter une opération explicite « réinitialiser la branche locale sur origin », derrière confirmation. Elle est sans danger ici : le stockage est compact, sans copie de travail, donc les fichiers des stacks ne sont pas touchés.

### 1.5 [MAJEUR] La sélection de stacks est ignorée dès qu'un Compose existe à la racine du lien

**Fichiers** : `core/internal/gitsync/binding.go:2126-2129` (peuplement) et `2191-2205` (test d'appartenance)

Pour un Compose situé à la racine du binding, `filepath.Dir` produit `"."`, qui atterrit dans `policy.selectedRoots`. Or `selectsPath` teste :

```go
if root == "." || relative == root || strings.HasPrefix(relative, root+"/") {
    return true, true
}
```

La branche `root == "."` renvoie donc **vrai pour tout chemin**, quel qu'il soit.

**Scénario** : un binding avec un Compose racine sélectionné et des stacks imbriquées explicitement désélectionnées (ou mises en pause « récupération »). Ces stacks restent dans l'inventaire ; un export du stack racine **pousse aussi leurs fichiers vers Git**, et l'import automatique peut écrire chez elles tant que la baseline correspond. Les conflits limitent la casse, mais la portée promise par l'interface est violée. La sémantique inverse est pourtant assumée ailleurs dans le même fichier (`binding.go:1421` : *« A root stack owns root files only »*). Les tests ne couvrent que la topologie sans Compose racine.

**Correction** : pour la racine `"."`, ne sélectionner que les fichiers sans `/` dans leur chemin relatif, et ne renvoyer `traverse` que pour atteindre les sous-racines effectivement sélectionnées.

### 1.6 [MINEUR] Un échec réseau efface les badges de conflit puis les repeint « à jour »

**Fichier** : `core/internal/gitsync/automation.go:479`

Le chemin d'erreur utilise `updateActiveStackStatuses` (variante **non préservante**) là où le reste du fichier utilise `updateActiveStackStatusesPreservingLocal` (lignes 292, 294, 511, 513). Les états `locally_deleted` et `orphaned` sont donc écrasés par `error`. Au cycle suivant, sur le même commit, `skippedStackScan` ne retrouve plus l'état local et repeint tout en `up_to_date` (lignes 509-513).

Pas de perte de données — l'import reste bloqué au prochain commit — mais **l'invite « restaurer / supprimer » disparaît de l'interface**, ce qui laisse croire que tout est réglé.

**Correction** : utiliser la variante préservante ligne 479.

### 1.7 [MINEUR] « Lancer maintenant » ne retente pas un déploiement partiel

**Fichier** : `core/internal/gitsync/automation.go:335-336` et `440-442`

`retryCurrentDeployment` inclut bien l'état `partial`, mais la réinjection des cibles ne couvre que `failed|pending`. Les stacks échouées d'un lot mixte ne sont donc **jamais** redéployées manuellement, contrairement à ce qu'annonce le commentaire de `RunBindingAutoSyncNow`.

### 1.8 [MINEUR] Le binding est lu avant la prise du verrou

**Fichier** : `core/internal/gitsync/automation.go:266` puis `277`

Le binding est chargé, **puis** `TryLock` est appelé. Une modification concurrente de la sélection Compose (qui tient le même verrou) peut aboutir entre les deux : l'exécution part alors avec une sélection périmée, et une stack tout juste désélectionnée est synchronisée puis déployée une dernière fois.

**Correction** : recharger le binding après acquisition du verrou.

### 1.9 [MINEUR] Résidus de staging de provisioning après un crash

**Fichier** : `core/internal/gitsync/provision.go:347-361`

Les suppressions provisionnées sont déplacées dans `.dockman-provision-staging-<uuid>` à l'intérieur du dossier de stack, purgé uniquement par `Commit` ou `Rollback`. Après un crash en plein déploiement, le dossier subsiste indéfiniment — invisible, puisque `shouldSkipPath` le masque de la synchronisation. `cleanupStaleTemporaryWorktrees` ne couvre que les `.dockman-export-` du workspace.

**Correction** : balayage équivalent au démarrage.

### 1.10 [MINEUR] Dépendance dure à `api.github.com` pour chaque opération SSH

**Fichier** : `core/internal/gitsync/repository.go:879`

`authForRepository` rappelle `githubHostKeys` à **chaque** fetch et push SSH. Une panne ou une limitation de débit de l'API GitHub casse donc la synchronisation SSH alors que le serveur git lui-même est parfaitement joignable. Un cache avec durée de vie suffirait.

### 1.11 [MINEUR] Code mort

- **Tout le paquet `core/internal/git/`** (611 lignes dans `service.go`, plus deux handlers) n'est importé nulle part — vérifié : aucun `import ".../internal/git"` hors `gitsync`. Le migrator qui l'utilisait est commenté (`app.go:136-139`). Il contient des écritures **non confinées** (`SyncFile`, `service.go:374-376` : `filepath.Join` sans validation). À supprimer avant qu'il soit réutilisé par mégarde. Le proto `spec/protos/git/v1/git.proto` (5 RPC) n'est enregistré nulle part côté Go.
- `pruneBindingBackups` (`binding.go:3156`) n'a aucun appelant de production ; la rétention passe par `pruneManagedBackups`.
- `RepositoryGitStatus.Clean` vaut toujours `true` (`repository.go:511`, jamais recalculé) — cohérent avec le stockage compact sans copie de travail, mais les gardes `!status.Clean` sont vestigiales et trompeuses à la lecture.

### 1.12 [MINEUR] Erreur avalée avant suppression de binding

**Fichier** : `core/internal/gitsync/handler_http.go:464` — `binding, _ := GetBinding` : si le binding n'existe pas, l'activité est enregistrée avec des identifiants vides.

---

## 2. Backend Docker et sécurité

### 2.1 Ce qui est sain (vérifié)

- **Navigateur de fichiers** : `cleanBrowserPath` rejette `..`, `\` et NUL avant tout usage ; le binaire helper est confiné par `os.OpenRoot`, donc les symlinks ne peuvent pas sortir de `/volume` ; le conteneur helper est créé sans réseau, avec `CapDrop: ALL` plus quatre capacités système de fichiers et `no-new-privileges` ; toutes les commandes passent en `argv` (jamais de shell) ; `chmod`/`chown` sont validés côté serveur **et** côté helper. Aucune traversée exploitable trouvée.
- **Durcissement HTTP** : politique d'origine, limites de corps, `ReadHeaderTimeout` et `MaxHeaderBytes` sont câblés en amont de CORS. Combinés au cookie `SameSite=Lax`, ils neutralisent en pratique le `CheckOrigin` permissif du WebSocket.
- **Cleaner** : la migration cron est correcte (colonne ajoutée, test présent), `normalizedPruneCron` est bien bordé, et `conservativeImageReclaimable` corrige réellement le calcul — les conversions `uint64()` du handler sont désormais sûres grâce au clamp.

### 2.2 [MAJEUR] Le terminal hôte contourne entièrement `DOCKMAN_ALLOW_SELF_EXEC=false`

**Fichiers** : `core/internal/docker/handler_http.go:48` (route `GET /shell`) → `core/internal/docker/hostshell_http.go:28-61`, `core/internal/docker/compose/shell.go:53-75`

Vérification : `checkExecAllowed` est appelé en `handler_http.go:66`, `:173` et `file_browser_http.go:276`, mais **jamais dans `hostshell_http.go`**. Pour l'hôte local, `Compose.StartShell` retombe sur `LocalRunner.StartShell`, qui exécute `/bin/bash` **dans le processus Dockman** — donc un PTY root dans le conteneur Dockman.

**Scénario** : sur une instance configurée avec `ALLOW_SELF_EXEC=false`, un appel à `/api/protected/local/docker/shell` donne exactement ce que la documentation (`website/docs/install/env.md`) promet d'interdire : accès à la configuration et aux credentials montés dans Dockman.

*Note de contexte* : cette fonctionnalité de terminal hôte a été livrée avant l'introduction du drapeau et n'a jamais été raccordée.

**Correction** : soit soumettre `/shell` (hôte local) au même contrôle, soit corriger la documentation pour cesser de présenter le drapeau comme une protection de la configuration Dockman.

### 2.3 [MAJEUR] Le mode debug crée un conteneur privilégié depuis n'importe quelle cible

**Fichiers** : `core/internal/docker/handler_http.go:195-210` → `core/internal/docker/debug/debug.go:98`

Le conteneur de debug est créé avec `Privileged: true`, `PidMode` et `NetworkMode` sur la cible, et **une image fournie par le client** (`?image=`), tirée automatiquement. `checkExecAllowed` ne contrôle que le conteneur *visé*, pas le conteneur de debug créé.

**Scénario** : `GET /exec/<n_importe_quel_conteneur>?debug=1&image=attaquant/img&cmd=/bin/sh` produit un conteneur privilégié (tous périphériques, toutes capacités), donc un accès au disque de l'hôte — et à la configuration Dockman, malgré la politique.

**Correction** : au minimum documenter que la politique n'est pas une frontière de sécurité ; au mieux restreindre le debug (liste d'images autorisées, non privilégié par défaut, drapeau dédié).

### 2.4 [MAJEUR] `ContainerRecreate` détruit un conteneur arrêté avant de savoir s'il peut le recréer

**Fichier** : `core/internal/docker/updater/updater.go:398-406`

```go
// container at rest: swap in place, leave it stopped
if !wasRunning {
    if _, err := u.cli().ContainerRemove(ctx, oldContainer.ID, ...); err != nil { ... }
    if _, err := u.containerCreate(ctx, imageTag, containerName, inspectedData); err != nil { ... }
    return nil
}
```

Sur ce chemin, **aucun rollback n'est possible** : l'ancien conteneur n'existe plus. Le chemin « en marche » (lignes 408-440) fait pourtant les choses correctement — création sous nom temporaire, healthcheck, puis suppression et renommage.

**Scénario** : mise à jour forcée d'un conteneur arrêté dont la configuration référence un réseau supprimé, ou dont le tag est devenu introuvable. La suppression réussit, la création échoue, **le conteneur et sa configuration sont définitivement perdus** ; seul un message d'erreur remonte.

**Correction** : appliquer le même schéma que le chemin « en marche » — créer d'abord sous un nom temporaire, puis supprimer l'ancien et renommer.

### 2.5 [MAJEUR] Le rollback de l'updater utilise un contexte déjà annulé

**Fichier** : `core/internal/docker/updater/updater.go:408-429`, `443-453`

`ContainerStop(ctx)` puis, en cas d'échec, `containerRollbackToOldContainer(ctx, ...)` → `ContainerStart(ctx, ...)`. Or `ctx` est celui du flux Connect (`handler_containers.go:239-250`).

**Scénario** : l'utilisateur ferme l'onglet pendant le healthcheck — qui peut durer plusieurs minutes, voir le point suivant. Le contexte est annulé, la création ou le démarrage échoue, **le rollback échoue immédiatement avec `context canceled`** — et l'ancien conteneur reste arrêté alors que le message annonce un retour arrière réussi.

**Correction** : `rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)` pour tout le chemin de compensation (stop, remove, start, rename).

### 2.6 [MAJEUR] `defer close(done)` manquant — l'annulation est morte-née pour toutes les commandes SSH

**Fichier** : `core/internal/docker/compose/runner.go:87-98`

```go
done := make(chan struct{})
go func() {
    select {
    case <-ctx.Done():
        fileutil.Close(session)   // ne s'exécute jamais
    case <-done:
    }
}()
close(done)                       // <- immédiat, avant session.Run
return session.Run(fullCmd)
```

`close(done)` s'exécute **avant** `session.Run`, donc la goroutine de surveillance prend systématiquement la branche `<-done` et `ctx.Done()` n'est jamais observé. `session.Run` bloque ensuite sans aucune possibilité d'interruption.

**Scénario** : sur un hôte SSH, un `docker compose pull` qui pend (registre injoignable) ne se termine jamais, même après déconnexion du client ; la session SSH et la goroutine restent vivantes indéfiniment. Concerne aussi `PullImage` (chemin de mise à jour des conteneurs) et les statistiques d'hôte distantes.

**Correction** : `defer close(done)` au lieu de `close(done)`.

### 2.7 [MINEUR] Healthcheck de l'updater : requête HTTP sans timeout vers une URL issue d'un label

**Fichier** : `core/internal/docker/updater/updater.go:577` (et `370`)

`endpoint := c.Config.Labels["dockman.update.healthcheck.ping"]` puis `http.Get(endpoint)` avec le client par défaut : **aucun timeout**, redirections suivies. Par ailleurs, `updater.go:532` utilise `context.Background()` et ignore le contexte de la requête, avec une temporisation calculée à 1,5 × une durée lue dans un label.

**Scénarios** : (a) un conteneur issu d'une image tierce compromise porte un label pointant vers `http://169.254.169.254/latest/meta-data/` ou une IP interne — Dockman émet alors la requête depuis le réseau de l'hôte ; (b) un label `uptime=24h` fait bloquer la RPC de mise à jour pendant 36 heures, avec un conteneur `_updated` en suspens et un rollback qui partira sur un contexte mort.

**Correction** : `http.Client{Timeout: …}` avec `http.NewRequestWithContext`, restriction du schéma et de l'hôte (loopback ou IP du conteneur), et plafonnement des durées lues dans les labels.

### 2.8 [MINEUR] Conteneurs helper du navigateur de fichiers non balayés au démarrage

**Fichier** : `core/internal/docker/file_browser_http.go:33` (label `dockman.file-browser.helper`) et `:342-366`

Le helper est créé avec `AutoRemove: false` et n'est nettoyé que par le `defer` du handler. Vérification : le label n'apparaît **qu'une seule fois** dans tout le code Go, donc aucun balayage n'existe ailleurs.

**Scénario** : l'utilisateur ouvre un volume puis clique « Redémarrer Dockman » — le processus meurt avant le `defer`, et un conteneur `dockman-file-browser-<hex>` survit indéfiniment **avec le volume monté en lecture-écriture**. Idem après un crash ou un OOM.

**Correction** : balayage au démarrage par label, sur le modèle de `CleanupSelfUpdateHelper`, et/ou `AutoRemove: true` avec une durée de vie maximale.

### 2.9 [MINEUR] Le binaire helper peut rester dans le conteneur cible

**Fichier** : `core/internal/docker/file_browser_http.go:385-399`, `:406-426`

Le binaire est copié puis testé ; il n'est supprimé que par `--unlink` lors de l'exécution réelle. Si `ExecCreate` ou `ExecAttach` échoue après l'installation, `/tmp/.dockman-file-helper-<hex>` reste en place, exécutable — ou, si le système de fichiers racine est en lecture seule, un fichier déposé **dans le volume de données de l'utilisateur**.

**Correction** : suppression explicite dans le `cleanup()` du `browserTarget` côté conteneur (aujourd'hui une fonction vide).

### 2.10 [MINEUR] Téléchargement et envoi contournent le confinement

**Fichier** : `core/internal/docker/file_browser_http.go:197` (`CopyToContainer`) et `:222` (`CopyFromContainer`)

Le démon résout les symlinks relativement à la **racine du conteneur**, pas à `/volume`. Un symlink `/volume/x -> /` rend `…/download?path=/x` capable d'archiver tout le système de fichiers du conteneur helper, alors que le listage — qui passe par le helper — refuserait. L'impact reste limité (helper éphémère, sans montage hôte), mais l'asymétrie avec la garantie annoncée mérite d'être corrigée : faire passer ces deux chemins par le helper, ou refuser les symlinks avant l'archivage.

### 2.11 [MINEUR] Cleaner : résultats d'erreur jamais enregistrés

**Fichier** : `core/internal/cleaner/service.go:293-323`

`result.Err` est renseigné, puis la fonction retourne **sans appeler `store.AddResult`** quand la configuration est désactivée (lignes 306-308) ou quand le client Docker est introuvable (lignes 310-314). L'historique affiché ne montre donc jamais ces échecs.

**Correction** : enregistrer le résultat dans un `defer`, ou avant chaque `return`.

### 2.12 [MINEUR] Migration cron : les anciens intervalles inférieurs à une heure désactivent silencieusement le nettoyage

**Fichier** : `core/internal/cleaner/service.go:186-214` et `119-132`

Un `Interval` hérité devient `@every 5m0s`, aussitôt rejeté par la règle « pas plus d'une fois par heure ». Au démarrage, `StartEnabled` se contente d'un `log.Warn` : le job n'est **jamais** planifié, alors que l'interface continue d'afficher la configuration comme active.

**Correction** : lors de la migration, remonter l'intervalle au minimum autorisé et persister l'expression, plutôt que d'échouer.

### 2.13 [MINEUR] Erreurs écrasées et panics potentiels

- `core/internal/docker/handler_volumes.go:146-149` : `err` est réassigné dans la boucle — **seule la dernière suppression de volume remonte son erreur**, les échecs précédents sont muets.
- `core/internal/docker/updater/updater.go:112`, `:298`, `:308`, `:314` et `core/internal/docker/container/stats_cache.go:180` : `Names[0]` sans garde — `Names` peut être vide sur un conteneur en cours de suppression, ce qui provoque une panique dans le handler.
- `core/internal/docker/updater/updater.go:222-224` : `WithConfig` affecte le paramètre local (`c = conf`) — **l'option n'a aucun effet**.
- `core/internal/docker/updater/updater.go:435-437` : si le renommage final échoue, le conteneur reste nommé `<name>_updated` tout en conservant les labels Compose → duplication au prochain `compose up`. Un simple `log.Warn` aujourd'hui.

### 2.14 [MINEUR] `ContainerUpdate` n'est pas couvert par le verrou de stack

**Fichier** : `core/internal/docker/handler_containers.go:239-250`

Contrairement à toutes les actions Compose (`withComposeActionLock`), la mise à jour par conteneur ne prend aucun verrou. Deux mises à jour concurrentes sur le même conteneur, ou une mise à jour pendant un `compose down`, s'entrelacent (stop → create `_updated` → remove) avec des résultats imprévisibles.

**Correction** : réutiliser `compose.TryLockStack` sur la stack résolue depuis le label `com.docker.compose.project.config_files`.

### 2.15 [MINEUR] Divers vérifiés

- `core/internal/docker/selfupdate.go:145-170` : `findSelfContainer` retombe sur **le premier** conteneur portant `dockman.container=true` si le préfixe du nom d'hôte ne correspond pas. Sur un hôte avec deux instances Dockman, « Redémarrer Dockman » peut redémarrer la mauvaise.
- `core/internal/docker/handler_http.go:26` : `CheckOrigin: func(r *http.Request) bool { return true }`. Neutralisé aujourd'hui par `enforceOriginPolicy` et `SameSite=Lax`, mais poser `DOCKMAN_ORIGINS=*` — valeur documentée historiquement comme défaut — remet `allowAll = true` et supprime la protection pour **toutes** les routes. À signaler dans la documentation, et à corriger en réutilisant `conf.GetAllowedOrigins()` dans `CheckOrigin`.
- Aucune échéance d'écriture sur les WebSockets (`pkg/ws/ws.go:64-70`, `hostshell_http.go:73`) : un client qui n'acquitte plus bloque `WriteMessage` sans limite, immobilisant la goroutine et le flux Docker associé.
- `core/internal/docker/container/stats_cache.go:53` et `container/events_hub.go:56` : les tables globales indexées par `*client.Client` ne sont jamais purgées à la déconnexion d'un hôte — fuite lente à chaque reconnexion.
- Rappel de contexte : `AUTH_ENABLE` vaut `false` par défaut (`auth/config.go:9`). Sans authentification, toutes les routes ci-dessus — terminal hôte, redémarrage, navigateur de fichiers — sont ouvertes.

---

## 3. Interface React

### 3.1 Ce qui est sain (vérifié)

- **Aucune mutation en place de données rendues** dans les zones revues : le regroupement Monitor est recomposé, `staleRows` et `rowBusy` sont copiés, le gel des statistiques attend une preuve fraîche du démon. Le contrat « Map fraîche à chaque render » du hook de statistiques a été vérifié **dans la sortie compilée**.
- **Store git-sync** remarquable : empreintes pour préserver les identités, instantanés vides gelés avec explication de l'erreur React #185, observateurs à comptage de références.
- **Visionneuse de logs** : tampon immuable plafonné, reprise par filigrane avec instantané de rejeu, `AbortController`, backoff.
- **MUI v9** : aucune propriété système hors `sx`, pas de `Grid` déprécié, `slotProps` généralisé, `Popover` et `Dialog` aux nouvelles API.

### 3.2 [MAJEUR] Quatre composants ne sont pas compilés par le React Compiler — cause : `try/finally`

Vérifié par **compilation réelle** avec `babel-plugin-react-compiler` 1.0 (le préréglage de `vite.config.ts`), journal du plugin à l'appui, puis recompté sur le code courant :

| Fichier | Statut | Cause | `finally` restants |
|---|---|---|---|
| `ui/src/pages/monitor/monitor-page.tsx:181` | **non compilé** | `TryStatement without a catch clause` | 2 |
| `ui/src/pages/settings/tab-git.tsx:268` | **non compilé** | `TryStatement with a finalizer` | 12 |
| `ui/src/components/git-stack-status.tsx:59` | **non compilé** | idem | 6 |
| `ui/src/components/git-binding-recovery.tsx:81` | **non compilé** | idem | 8 |
| `ui/src/pages/monitor/monitor-table.tsx` | compilé (7 composants) | — | 0 |
| `ui/src/pages/compose/components/logs-panel.tsx` | compilé | — | 0 |

Le compilateur sait traiter `try/catch`, mais il **abandonne** sur un `try/finally` ou un `try` sans `catch` dans le corps d'un composant.

**Scénario Monitor** : la page se re-rend à chaque vidange du flux de statistiques (fenêtre de 200 ms, plusieurs fois par cycle de 5 secondes), à chaque tic d'uptime (10 secondes) et à chaque interaction. Non compilée, elle **recrée tous ses callbacks inline** (`onToggleExpand`, `onScroll`, `onRowDetails`, `handleRowAction`, `onStackOutput`…). `MonitorTable` *est* compilée, mais ses propriétés changent d'identité à chaque render parent, donc **toute la table se re-rend** — ligne de stack et toutes les lignes de conteneurs. Sur un hôte à cinquante conteneurs et plus, c'est l'essentiel du coût processeur de la page. Seuls les sous-arbres `Sparkline` sont épargnés, les tableaux d'historique gardant leur identité entre deux échantillons.

*Note de contexte* : dans Monitor, les deux `try/finally` sont ceux de `containerAction` (ligne 534) et `handleRefresh` (ligne 454), introduits par le lot « verrous de boutons + spinner de rafraîchissement ».

**Correction** : sortir la logique `try/finally` du corps des composants. Deux techniques, toutes deux sans risque :

- un helper au niveau module — `async function withBusy(setBusy, ids, fn) { … }` ;
- remplacer `try { await x(); } finally { y(); }` par `await x().finally(y)` — la **méthode** `.finally()` d'une promesse ne pose aucun problème, seule la **structure** `try/finally` déclenche l'abandon.

Après refactor, revérifier avec le journal du plugin que `CompileSuccess` apparaît bien.

### 3.3 [MAJEUR] `LogRow` non compilé à cause d'un littéral BigInt

**Fichier** : `ui/src/components/log-viewer/logs-viewer.tsx:201` (composant déclaré ligne 142)

Abandon vérifié : `Handle BigIntLiteral expressions`, causé par `entry.timeNano !== 0n`. `LogsViewer` est compilée, mais quand `entries` change — à chaque vidange, soit au plus toutes les 80 ms en flux soutenu — le `displayed.map(...)` produit de nouveaux éléments, et `LogRow`, non mémoïsée, ré-exécute l'analyse et le rendu des segments pour les 2 000 lignes du tampon.

**Correction, une ligne** : hisser le littéral au niveau module — `const NANO_ZERO = 0n;` — puis comparer à `NANO_ZERO`. L'abandon ne concerne que les littéraux **à l'intérieur** de la fonction compilée. C'est le composant le plus chaud de l'application (logs Monitor, dialog de détails, panneau Compose).

### 3.4 [MAJEUR] Le dialog de détails renvoie sur « Overview » à chaque action

**Fichier** : `ui/src/pages/monitor/container-details-dialog.tsx:566-577`

```ts
const load = useCallback(async () => { … }, [client, containerID, containerState]);
useEffect(() => { if (open) { setTab('overview'); setProcessCount(null); void load(); } }, [open, load]);
```

`load` dépend de `containerState`, et l'effet dépend de `load`.

**Scénario** : ouvrir le dialog d'un conteneur en marche, aller sur l'onglet **Logs** ou **Exec**, cliquer **Restart** dans l'en-tête. L'état passe par `restarting` puis `running` → `load` change deux fois d'identité → **deux retours forcés sur Overview** en pleine consultation, et la session exec affichée est masquée. Même effet pour stop, pause, unpause, ou une transition d'état externe pendant que le dialog est ouvert.

**Correction** : scinder en deux effets — `[open, containerID]` pour le reset d'onglet, `[open, load]` pour le rechargement de l'inspect seul.

### 3.5 [MAJEUR] La recherche ne purge pas la sélection — suppression groupée sur des lignes invisibles

**Fichier** : `ui/src/pages/monitor/monitor-page.tsx:407-419` (filtre) contre `:944` (recherche)

`changeStateFilter` purge explicitement les deux sélections, avec un commentaire qui énonce la règle :

> *Never leave hidden rows selected: bulk actions must only target containers the operator can currently see.*

Mais `setSearch` est passé tel quel à `MonitorTable` via `onNameSearchChange`, **sans aucune purge**.

**Scénario** : sélectionner six conteneurs, taper une recherche qui n'en laisse qu'un visible, cliquer **Remove** dans la barre d'outils → les six sont supprimés, dont cinq invisibles. Le compteur « 6 selected » atténue le risque, mais l'invariant est violé précisément sur l'action la plus destructive.

**Correction** : appliquer la même purge au changement de recherche, ou à défaut intersecter la sélection avec les identifiants visibles au moment d'exécuter l'action groupée.

### 3.6 [MINEUR] Ouvrir Monitor détruit les sessions logs/exec de la vue Compose

**Fichier** : `ui/src/pages/monitor/monitor-page.tsx:246`

L'effet `clearTabs()` s'exécute aussi au montage, inconditionnellement. Le store d'onglets étant global, des sessions exec ou logs ouvertes depuis la vue Compose sont fermées — WebSockets comprises — dès qu'on ouvre Monitor, **même sans changement d'hôte**. Le panneau de logs implémente pourtant exactement le bon motif (`prevHost`, `logs-panel.tsx:78-84`) pour ne purger que sur changement d'hôte.

**Correction** : aligner Monitor sur ce motif.

### 3.7 [MINEUR] Render superflu à chaque interrogation des conteneurs

**Fichier** : `ui/src/pages/monitor/monitor-page.tsx:404-406`

`setStateFilters(current => current.filter(...))` retourne une nouvelle identité **même quand rien n'est retiré** → re-render à chaque rechargement, soit toutes les 2 secondes en phase de stabilisation. Retourner `current` quand la longueur est inchangée.

### 3.8 [MINEUR] Écriture de ref pendant le render

**Fichier** : `ui/src/hooks/docker-containers-stats.ts:243`

`sortRef.current = {field: sortField, order: sortOrder};` au niveau du corps du hook : violation des règles de React, et obstacle supplémentaire à la compilation du hook. À déplacer dans un `useEffect`, la boucle de flux lisant la ref de façon asynchrone.

### 3.9 [MINEUR] `history: new Map(statHistories)` — contrat fragile

**Fichier** : `ui/src/hooks/docker-containers-stats.ts:458`

État actuel vérifié : le hook est abandonné par le compilateur (`try/finally` et `for await`), donc la Map est bien recréée à chaque render — le comportement est correct **aujourd'hui**. Deux réserves :

1. si un refactor futur rend le hook compilable, cette lecture d'un global mutable pendant le render devient éligible à une mise en cache indue → sparklines gelées, exactement le bug que le commentaire cherche à éviter ;
2. l'identité neuve à chaque render invalide le `useMemo` de `groups` (`monitor-page.tsx:338`) à **chaque** render, pas seulement quand des points arrivent.

**Correction** : exposer un compteur de version d'historique (state incrémenté dans `recordStat`) et dériver `history` par `useMemo([version])`.

### 3.10 [MINEUR] `useHostStats` continue d'interroger l'hôte onglet caché

**Fichier** : `ui/src/hooks/docker-containers-stats.ts:489-525` — intervalle de 5 secondes sans garde `document.visibilityState`. Vérifié : aucune occurrence de `visibilityState` dans le fichier, alors que l'observateur git (`git-stack-status-store.ts:121`) implémente la garde.

### 3.11 [MINEUR] Divers vérifiés

- `logs-viewer.tsx:291-294` : `localStorage.setItem` **à l'intérieur** de l'updater de `setState` — les updaters doivent être purs ; double écriture en mode strict.
- `container-details-dialog.tsx:356` et `logs-panel.tsx:26` : `useRef(new FitAddon())` instancie un objet jeté à chaque render. Initialisation paresseuse.
- `monitor-page.tsx:936` : `onToggleAllContainers={(ids, on) => toggleContainers(ids, on)}` — wrapper inutile et propriété redondante avec `onToggleContainers`.
- `container-details-dialog.tsx:644` : `JsonInspect` est monté dès l'ouverture du dialog (Box `hidden`) — un inspect de plusieurs milliers de lignes est coloré et rendu alors qu'on regarde Overview. Le garder démonté jusqu'à la première visite de l'onglet.
- `use-logs-stream.ts:174` : le minuteur de backoff (jusqu'à 30 secondes) n'est pas annulé au nettoyage.
- `git-stack-status-store.ts:119-120` : un appel hors observateur crée une entrée `{references: 0}` jamais supprimée (borné par le nombre d'hôtes).
- `tab-git.tsx:307`, `1135-1136`, `1147` : indentation par tabulations au milieu d'un fichier en espaces.

### 3.12 MUI v9 — deux résidus

- `monitor-table.tsx:293` : `inputProps={{'aria-label': …}}` sur `InputBase` — dernier survivant de l'ancienne API dans les fichiers revus ; passer à `slotProps`.
- `logs-viewer.tsx:446-449`, `610-612` : `sx={[{…}, ...(Array.isArray(x) ? x : [x])]}` — artefact de codemod ; `sx={[{…}, x]}` suffit.

---

## 4. Segmentation

Le code est correctement organisé par domaine ; le problème est la **taille de quatre fichiers** et quelques duplications entre eux.

### 4.1 `ui/src/pages/settings/tab-git.tsx` (1 447 lignes, ~20 dialogs, ~45 états)

Découpage proposé en six fichiers plus deux partagés :

1. **`ui/src/lib/git-api.ts`** *(nouveau, partagé)* : `api<T>` / `APIError`, `dateLabel`, `comparisonLanguage`, `statusColor`, `authLabel`. Élimine les **trois copies** du wrapper `fetch` (`tab-git.tsx:173-183`, `git-binding-recovery.tsx:58-68`, `git-stack-status.tsx:15-24`) et les **deux versions divergentes** de `comparisonLanguage` — celle de `tab-git` gère `.sh`, `.sql` et `.md`, celle de la récupération non ; un import unique corrige la divergence.
2. **`settings/git/git-types.ts`** : toutes les interfaces (~140 lignes).
3. **`settings/git/compose-path-selector.tsx`** : déjà autonome.
4. **`settings/git/repositories-section.tsx`** (~380 lignes) : table des dépôts et dialogs associés.
5. **`settings/git/credentials-section.tsx`** (~180 lignes).
6. **`settings/git/bindings-section.tsx`** (~420 lignes) : table des liens et dialogs binding / politique / automatisation / sélection Compose.
7. **`settings/git/transfer-dialog.tsx`** (~450 lignes) + **`use-transfer-preview.ts`** : les ~18 états du flux preview/transfert regroupés dans un reducer, avec les quatre sous-dialogs.
8. **`settings/git/use-git-deeplink.ts`** : l'effet de lien profond, en **réutilisant `previewTransfer`** au lieu de la duplication inline actuelle (qui recopie sa logique avec de subtiles différences — source de dérive).
9. Partagé : **`git-comparison-dialog.tsx`** — le dialog DiffEditor existe en double quasi identique.

### 4.2 `ui/src/pages/monitor/monitor-page.tsx` (996 lignes)

1. **`monitor/monitor-model.ts`** : mémoire de vue, préférences locales, `monitorStackKey`, `rowSortValue`, `groupSortValue`, `aggregateStack`, `sumSeries` — pur, testable.
2. **`monitor/use-monitor-actions.ts`** (~280 lignes) : `rowBusy`, `staleRows`, effet de purge, `containerAction`, `startContainerUpdate`, actions de stack. **C'est ici qu'on isole les `try/finally`, ce qui rend MonitorPage compilable** (§ 3.2).
3. **`monitor/use-monitor-selection.ts`** : sélections mutuellement exclusives et purges — filtres **et** recherche (§ 3.5).
4. **`monitor/monitor-toolbar.tsx`** (~230 lignes) : barre d'outils et définitions des actions groupées.
5. Reste (~300 lignes) : assemblage, `groups`, `flatRows`, `detailsRow`.

### 4.3 `ui/src/pages/monitor/monitor-table.tsx` (884 lignes)

1. **`monitor/monitor-types.ts`** : types et contrat de propriétés — casse aussi l'import croisé `container-details-dialog → monitor-table`.
2. **`monitor/stack-row.tsx`** : `StackRow`, `StackActionButton`, `RedeployMenuButton`.
3. **`monitor/container-row.tsx`** : `ContainerRow`, `StateCell`, popover de confirmation.
4. **`monitor/metric-cell.tsx`** et **`lib/uptime.ts`** : `formatUptime` est **dupliqué** avec `container-stat-table.tsx:396` (implémentations identiques).
5. Reste (~250 lignes) : table, en-tête triable, recherche.

### 4.4 `ui/src/pages/monitor/container-details-dialog.tsx` (661 lignes)

1. **`monitor/details/inspect-helpers.ts`** : helpers de lecture d'inspect.
2. **`monitor/details/exec-terminal.tsx`** : à fusionner avec `exec-launch-popover.tsx` — la récupération des options de shell et le choix contexte/root/nobody/autre sont **dupliqués** ; extraire `useExecShellOptions(containerID)`.
3. **`monitor/details/json-inspect.tsx`** : avec montage paresseux (§ 3.11).
4. **`monitor/details/overview-tab.tsx`** : et extraire la détection des domaines Traefik, **même expression régulière** qu'en `monitor-table.tsx:582`.
5. Reste (~250 lignes) : coque du dialog et onglets courts. Le popover « Remove ? », dupliqué avec `ContainerRow`, devient un `ConfirmRemovePopover` commun.

### 4.5 `ui/src/components/log-viewer/logs-viewer.tsx` (731 lignes)

Taille acceptable ; **priorité au correctif BigInt** (§ 3.3). Si découpage : `log-row.tsx` (avec la constante hissée) et `logs-toolbar.tsx`, le viewer gardant défilement, recherche et état.

`git-stack-status.tsx` (314 lignes) et `git-binding-recovery.tsx` (324 lignes) sont cohérents en l'état ; seuls les helpers partagés en sortent.

---

## 5. Infrastructure : CI et modèle de branches

### 5.1 État des workflows

Deux familles cohabitent :

- **Workflows du fork**, épinglés par SHA et à permissions minimales : `fork-checks.yml`, `fork-integration-build.yml`, `ghcr-cleanup.yml`, `fork-proto-gen.yml` (partiellement épinglé). Build multi-architectures avec SBOM, provenance `mode=max`, signature cosign sans clé, garde Trivy sur les vulnérabilités HIGH/CRITICAL corrigeables.
- **Workflows hérités d'upstream**, à tags mutables : `action-docker.yml`, `build-canary.yml`, `release-build.yml`, `build-release-tag.yml`, `label-comment.yml`.

La rétention GHCR (`scripts/ghcr-retention.py`) a été **validée contre le registre réel** : exactement trois historiques retenus, chacun avec ses tags par architecture et son tag de référence de signature protégé. Les tags de publication sont protégés, le nettoyage est refusé si les tags requis manquent, et une vérification a lieu après suppression. Aucun défaut trouvé.

### 5.2 [MAJEUR] Les squashes de PR upstream embarquent les fichiers CI du fork

Vérifié :

- `79697b6` (branche `agent/upstream-01-core-foundations`) ajoute `.github/workflows/fork-checks.yml` (136 lignes), `fork-integration-build.yml` (204), `fork-proto-gen.yml` (84), `.github/dependabot.yml` (52) et `.trivyignore.yaml` (8) ;
- `d065ad0` (branche `agent/upstream-06-integration-delivery`) modifie `fork-integration-build.yml` et `ghcr-cleanup.yml`, et ajoute `scripts/ghcr-retention.py` (319 lignes) — la rétention du namespace `cerede2000`, sans objet chez l'upstream.

C'est en contradiction directe avec le principe affiché en tête de ces mêmes workflows : les branches de contribution restent vierges pour des PR sans conflit.

**Correction** : purger les fichiers CI du fork de ces deux squashes avant toute ouverture de PR.

### 5.3 [MAJEUR] Divergence pnpm avec l'upstream

`upstream/main` = point de fork + un commit : `853bc29 chore: switch to pnpm and update deps` — suppression de `ui/package-lock.json` (11 143 lignes), ajout de `ui/pnpm-lock.yaml` et `pnpm-workspace.yaml`, `pkg/docker/Dockerfile` passé de npm à pnpm, rafraîchissement de `core/go.mod` et `go.sum`.

Le fork est resté sur npm avec ses propres montées de version (MUI 9, TypeScript 6, Vite 8, Electron 43, xterm 6, eslint 10, react-router 8).

**Conséquence** : toute PR touchant aux dépendances rencontrera un conflit **modify/delete** sur le lockfile (supprimé en amont), un conflit direct sur `package.json` et sur le Dockerfile.

**Décision à prendre** :

- soit le fork migre aussi à pnpm — cela aligne Dockerfile et lockfile, mais impose d'adapter `fork-checks.yml` (cache npm → pnpm, `pnpm install --frozen-lockfile`, audit pnpm) et de vérifier electron-builder sous pnpm ;
- soit le fork reste sur npm et retraduit chaque changement de dépendances en `pnpm-lock.yaml` au moment des PR — double maintenance, avec risque de dérive de résolution entre les deux lockfiles.

Dans les deux cas, la pile `agent/upstream-01..06` doit être rebasée sur `853bc29` avant toute PR.

### 5.4 Modèle de branches

`integration` est une **pile linéaire**. Les branches `feat/*` et `fix/*` sont des **étiquettes posées sur cette pile** : elles contiennent chacune tout l'historique empilé (382 à 463 commits depuis le point de fork) et ne sont donc **pas proposables telles quelles** en amont. Les véhicules de PR sont les six branches `agent/upstream-01..06` — squashes propres basés exactement sur le point de fork.

**Branches non mergées dans `integration` (6)** : `agent/upstream-01-core-foundations` (1 commit), `-02-realtime-observability` (2), `-03-build-unified-monitor` (3), `-04-runtime-security-details` (4), `-05-filesystem-browsers` (5), `-06-integration-delivery` (6).

**Toutes les autres (20) sont contenues dans `integration`** : les neuf `feat/git-sync-*` et les onze `fix/*`.

[MINEUR] `fix/compose-only-allowlist-walk` est **locale uniquement** — vérifié absente d'`origin` — alors qu'elle pointe sur le tip d'`integration`. À pousser pour respecter le modèle « branche prête au chaud ».

### 5.5 [MINEUR] Défauts de workflows

- **Injection shell via les entrées de dispatch** : `fork-checks.yml:94` (`go test ${{ github.event.inputs.test_path }}`), `fork-integration-build.yml:62-63`, `fork-proto-gen.yml:73` et `:84`, `action-docker.yml:166-183`. Exploitable seulement par un compte disposant du droit de dispatch. **Correction** : passer les entrées par `env:` puis les citer (`"$VAR"`).
- **`fork-proto-gen.yml` incohérent** : actions à tags mutables (`checkout@v4`, `setup-go@v5`, `setup-node@v4`) et `buf@latest` non épinglé, alors que c'est le workflow **le plus sensible** (`contents: write`) et que son en-tête affirme le contraire. Risque de dérive de génération de code.
- **Workflows hérités non épinglés** : `build-release-tag.yml` utilise une action tierce à tag mutable avec `contents: write`, plus `npx semantic-release` non versionné.
- **`build-canary.yml`** : filtre `paths` obsolète (`Dockerfile` à la racine n'existe plus, le vrai est `pkg/docker/Dockerfile`) ; surtout, toute synchronisation de `main` poussée sur `origin` déclencherait un build `ghcr.io/cerede2000/dockman:canary` non désiré — tag ensuite protégé à vie par la rétention. À neutraliser sur le fork.
- **`action-docker.yml`** : `cache-to: type=gha` sans `scope` → les deux bras de la matrice s'écrasent mutuellement le cache. Le workflow du fork fait correctement `scope=<tag>-<arch>`.

Aucun secret exposé. `GITHUB_TOKEN` partout, sauf un PAT dans `build-release-tag.yml`, inerte tant que personne ne pousse sur `release`.

### 5.6 [MINEUR] `docs/examples/provision.yml` non versionné

Manifeste de provisioning valide, conforme au schéma implémenté dans `core/internal/gitsync/provision.go`. La fonctionnalité et son plan de test français sont déjà versionnés, mais rien ne référence `docs/examples/`. À committer avec les autres documents français propres au fork.

---

## 6. Annexe — méthode de vérification

- **Findings React Compiler** : compilation réelle de chaque fichier avec `babel-plugin-react-compiler` 1.0 et lecture du journal du plugin, puis recomptage des `try/finally` et des littéraux BigInt sur le code courant.
- **Findings Go** : lecture du code aux lignes citées, plus recherches croisées pour établir les absences (aucun `context.WithTimeout` dans `gitsync`, aucune route de réinitialisation, label helper présent une seule fois, paquet `internal/git` jamais importé).
- **Findings CI** : lecture des workflows, extraction des journaux d'exécution réels, et interrogation du registre GHCR pour valider la rétention.
- **Findings branches** : `git merge-base`, `git branch --merged`, `git ls-remote`, inspection du contenu des squashes.

Les éléments non reproductibles ou spéculatifs ont été écartés. Chaque finding conservé cite un fichier et une ligne vérifiés à la date du document.
