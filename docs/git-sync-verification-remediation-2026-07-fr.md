# Vérification indépendante de la remédiation — état et reste à traiter

**Date** : 29 juillet 2026
**Base vérifiée** : `integration` @ `3162ed0`
**Méthode** : relecture ligne à ligne du code après remédiation, chaque point revalidé individuellement. Aucune affirmation reprise d'un rapport antérieur sans nouvelle vérification.
**CI** : Fork Checks et Fork Integration Build **verts** — les deux erreurs de compilation signalées précédemment sont résolues, l'allowlist govulncheck et l'audit npm passent.

---

## 1. Confirmé corrigé — ne pas retraiter

Chaque ligne ci-dessous a été vérifiée dans le code à `3162ed0`.

### Fiabilité et sécurité backend

| Point | Vérification |
|---|---|
| Annulation de contexte morte sur les commandes SSH | `defer close(done)` en place — `core/internal/docker/compose/runner.go:96` |
| Terminal hôte contournant `ALLOW_SELF_EXEC` | Garde ajoutée — `core/internal/docker/hostshell_http.go:35` |
| `ContainerRecreate` détruisant un conteneur arrêté avant de savoir le recréer | Refait en create-before-remove sous nom temporaire, nettoyage du remplaçant si la suppression échoue, contexte détaché pour le renommage — `core/internal/docker/updater/updater.go:419-441` |
| Rollback de l'updater sur contexte annulé | `rollbackContext(ctx)` détaché, utilisé sur tous les chemins de compensation |
| Healthcheck : `http.Get` sans timeout, SSRF via label | `NewRequestWithContext` + `Timeout: 15s` + `validateHealthcheckHost` **revalidé à chaque redirection** — `updater.go:638-676` |
| `ContainerUpdate` hors verrou de stack | `withContainerUpdateLocks` + `compose.TryLockStack` — `handler_containers.go:242-275` |
| Erreurs de suppression de volumes écrasées dans la boucle | Agrégation par `deleteErrors` — `handler_volumes.go:147-152` |
| Panics potentiels sur `Names[0]` | Gardes `summaryName` (`updater.go:370`) et `IdentityStats` (`stats_cache.go:176`) |
| Résultats d'échec du cleaner jamais enregistrés | `AddResult` appelé sur les chemins d'erreur — `cleaner/service.go:313,323` |
| Conteneurs helper du navigateur de fichiers non balayés | `CleanupFileBrowserHelpers` appelé au démarrage — `app/app.go:131` |
| Paquet mort `core/internal/git/` (1 131 lignes, écritures non confinées) | Supprimé |

### Synchronisation Git

| Point | Vérification |
|---|---|
| Aucun timeout réseau | `gitNetworkOperationTimeout = 3 min` + helper `gitNetworkContext`, **11 sites d'appel** couvrant fetch, push, clone et l'API GitHub — `gitsync/service.go:29-33` |
| Binding lu avant la prise du verrou | Rechargé **après** `TryLock`, avec commentaire explicatif — `gitsync/automation.go:264-277` |
| Chemin d'erreur écrasant les états `locally_deleted` / `orphaned` | Utilise désormais `updateActiveStackStatusesPreservingLocal` — `gitsync/automation.go:489` |
| **Sélection racine `"."` sélectionnant tous les chemins** | Corrigé : un Compose à la racine ne sélectionne plus que les fichiers de la racine, `continue` au lieu de `return true` — `gitsync/binding.go:2211-2218` |
| Récupération d'un dépôt divergent | Action « Reset to remote » avec confirmation `RESET LOCAL GIT STATE` |
| Règle d'inclusion **par nom de fichier** rouvrant tout l'arbre | Corrigé — seules les règles contenant un chemin peuvent autoriser la traversée, `explicitIncludeCanMatchBelow` filtre sur `!rule.basename` — `gitsync/binding.go:2415-2418` |

> Ce dernier point était plus grave que ce que la revue initiale décrivait : avant correction, **toute** règle de type nom de fichier (`application.conf`) faisait retourner `true` pour n'importe quel dossier, donc rouvrait l'arborescence complète.

### Interface

| Point | Vérification |
|---|---|
| 4 composants abandonnés par le React Compiler (`try/finally`) | `monitor-page.tsx`, `tab-git.tsx`, `git-stack-status.tsx`, `git-binding-recovery.tsx` : **0 occurrence** de `} finally {` |
| `LogRow` abandonné à cause d'un littéral BigInt | Plus de `0n` dans `components/log-viewer/logs-viewer.tsx` |
| Dialog de détails réinitialisant l'onglet à chaque action | Effet scindé : reset sur `[open, containerID]`, rechargement sur `[open, load]` — `container-details-dialog.tsx:578-583` |
| Recherche Monitor ne purgeant pas la sélection | `onNameSearchChange` purge les deux sélections — `monitor-page.tsx:946-950` |

---

## 2. Reste à traiter

### 2.1 [MAJEUR] Fail-open quand le catalogue Compose est vide

**Fichiers** : `core/internal/gitsync/binding.go:2408` et `binding.go:1836`

Deux tests inchangés, qui se composent :

```go
// 2408 — traversesComposeOnlyDirectory : traverse TOUT
if len(policy.compose) == 0 || policy.containsCompose(directory) {
    return true
}

// 1836 — autoExcludeLargeDirectory : désactive l'exclusion automatique
if relative == "" || len(policy.compose) == 0 || observed <= maxAutoDirectoryFiles || policy.containsCompose(relative) {
    return false
}
```

Vérifié : **aucune garde en amont** ne contrôle que le catalogue est peuplé avant de lancer les walkers (`grep` sur `len(binding.ComposePaths)` / `ComposePaths == ""` : aucun résultat hors des quatre sites ci-dessus).

**Conséquence** : si `ComposePaths` est vide pour un folder link, le profil `compose_only` se comporte comme `all_files` **et** perd son garde-fou. Le balayage descend dans les dossiers de données et de secrets, puis échoue sur la limite des 20 000 fichiers ou des 2 Gio. Le message d'erreur affirme alors que les gros dossiers sont ignorés automatiquement — ce qui est faux dans cet état précis.

Ce point est important parce que le catalogue et la sélection affichée dans l'interface sont **deux colonnes distinctes** : le sélecteur de stacks du dialog liste les stacks découvertes en direct, tandis que le filtrage de synchronisation lit `ComposePaths` en base. On peut donc voir « toutes mes stacks sélectionnées » à l'écran avec un catalogue vide côté serveur — typiquement si la découverte n'a rien trouvé à la création du lien et qu'aucun rafraîchissement n'a eu lieu depuis, celui-ci n'étant pas automatique.

C'est le seul chemin restant qui explique le symptôme rapporté en production : dossier `secret` embarqué dans la synchronisation en `compose_only`, sans aucune règle d'inclusion, suivi d'une erreur.

**Correction proposée**

1. En profil `compose_only`, ne plus renvoyer `true` sur catalogue vide dans `traversesComposeOnlyDirectory` — un mode restrictif doit fermer quand l'information manque, pas ouvrir.
2. Retirer `len(policy.compose) == 0` de la condition d'`autoExcludeLargeDirectory` pour que l'exclusion automatique reste active dans tous les cas.
3. Remonter une erreur explicite en amont du walker plutôt qu'un balayage complet : *« aucune stack Compose cataloguée pour ce lien — rafraîchissez le catalogue »*.

Les sites 2354 (`protectsCompose`) et 2366 (`protectsProvision`) laissent aussi passer `len(policy.compose) == 0`, mais dans le sens protecteur : à laisser tels quels.

### 2.2 [MOYEN] `compose_only` filtre sur le nom de fichier, pas sur les stacks détectées

**Fichiers** : `core/internal/gitsync/binding.go:2327` contre `binding.go:2354`

```go
// includesFile — profil compose_only
return isComposePath(relative) || matchesIgnoreRule(composeOnlyRules, relative, false)
```

`isComposePath` (`binding.go:2527`) est un simple test de nom de base : `compose.yml`, `compose.yaml`, `docker-compose.yml`, `docker-compose.yaml`. Il ne consulte jamais le catalogue — alors que `protectsCompose`, juste à côté, vérifie bien l'appartenance à `policy.compose`.

L'audit interne (`docs/git-sync-revue-complete-2026-07-fr.md`) valide cette ligne sous l'intitulé « Compose-only limité aux vrais **noms** Compose ✅ ». Le besoin exprimé côté exploitation est différent : synchroniser **les Compose réellement détectés comme stacks**, pas les fichiers qui en portent le nom.

Depuis l'ajout de la porte de traversée, l'exposition pratique est étroite — on n'entre plus que dans les ancêtres de Compose catalogués. Mais elle redevient totale dès que le catalogue est vide, puisque les deux défauts se composent avec le point 2.1.

**Décision à prendre** : soit aligner `includesFile` sur `protectsCompose` en faisant porter l'allow-list sur le catalogue, soit assumer la sémantique par nom et l'écrire explicitement dans l'aide de l'interface. La première option est cohérente avec le reste du moteur et avec ce que promet le libellé du profil.

### 2.3 [MINEUR] Trois points non traités

- **Résidus de staging de provisioning** — les dossiers `.dockman-provision-staging-<uuid>` survivent à un crash en plein déploiement, invisibles puisque masqués de la synchronisation par `shouldSkipPath`. `cleanupStaleTemporaryWorktrees` ne couvre que les `.dockman-export-`. Vérifié : aucun balayage équivalent au démarrage.
- **`CheckOrigin` renvoie toujours `true`** sur les WebSockets (`docker/handler_http.go:26`, `files/handler_http.go:269`). Neutralisé en pratique par `enforceOriginPolicy` et `SameSite=Lax`, mais poser `DOCKMAN_ORIGINS=*` remettrait `allowAll = true` et supprimerait la protection pour toutes les routes.
- **Aucun ping/pong WebSocket** dans tout `core/` — seules protections : deadline d'écriture de 15 s et `SetReadLimit`.

---

## 3. Tests à ajouter

Aucun test ne couvre aujourd'hui les cas suivants, tous liés au point 2.1 :

1. **Catalogue vide en `compose_only`** — un binding dont `ComposePaths` est vide ne doit pas descendre dans les sous-dossiers, et doit produire une erreur explicite plutôt qu'un balayage complet.
2. **Exclusion automatique des gros dossiers avec catalogue vide** — vérifier que le garde-fou reste actif.
3. **Compose non catalogué dans un dossier traversé** — un fichier nommé `compose.yml` hors catalogue ne doit pas entrer dans l'inventaire si la décision 2.2 retient la sémantique « catalogue ».

Le test existant sur les règles d'inclusion (`binding_test.go:1119`) ne couvre qu'un motif de type nom de fichier (`application.conf`). Un cas avec chemin (`config/*.conf`) mériterait d'être ajouté pour verrouiller la correction apportée à `explicitIncludeCanMatchBelow`.

---

## 4. Diagnostic terrain

Pour confirmer qu'une instance est bien dans le cas 2.1 :

```bash
curl -s http://<dockman>/api/protected/git/bindings | jq '.[] | {stackPath, subPath, syncProfile, composePaths}'
```

Si `composePaths` est vide ou ne contient pas les stacks attendues alors que l'interface affiche des stacks sélectionnées, le diagnostic est établi. Le bouton **« Refresh compose catalog »** du folder link doit alors faire disparaître l'erreur immédiatement — test rapide et non destructif.
