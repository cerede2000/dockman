# Plan de correction détaillé — suites de la revue du 2026-08

Document de travail. Chaque lot est livrable indépendamment, sur sa branche dédiée,
mergé sur `integration` dès que sa CI est verte.

Référence des constats : [revue-complete-secrets-updates-notifications-2026-08-fr.md](revue-complete-secrets-updates-notifications-2026-08-fr.md).

---

## 0. Cadre imposé et comment il est tenu

Trois contraintes gouvernent tout ce qui suit. Elles ne sont pas des intentions :
chaque correctif ci-dessous indique explicitement comment il les respecte.

### 0.1 Aucune régression

Protocole appliqué à chaque correctif, sans exception :

1. **Additif ou strictement encadré.** Un correctif n'ajoute jamais un chemin
   d'échec là où il n'y en avait pas. Quand il restreint un comportement
   (§1.3 classification protégée), la restriction est **annulable par un label
   explicite** : l'utilisateur reprend la main sans modifier le code.
2. **Chemins existants intacts.** Les gardes actuelles (`verifyHealth == false`,
   labels `dockman.update.healthcheck.*`, absence de runtime hôte) conservent
   exactement leur comportement d'aujourd'hui. Les nouveaux contrôles s'ajoutent
   à côté, jamais à la place.
3. **Tests existants non réécrits.** Un test n'est modifié que s'il affirme le
   comportement bogué. Chaque cas est listé nommément dans le lot concerné
   (aujourd'hui : aucun, les bugs visés ne sont couverts par aucun test).
4. **CI verte avant merge.** Aucun merge sur `integration` sans `Fork Checks`
   **et** `Fork Integration Build` au vert sur la branche dédiée.
5. **Cahier de test manuel** pour ce que la CI ne peut pas couvrir : systemd,
   tmpfs, vrai démon Docker. Fourni à la fin de chaque lot.

### 0.2 Overhead CPU/RAM minimal

Règle de conception : **aucun correctif n'introduit de polling.** Là où un état
doit être attendu, on s'abonne à un flux existant ou on utilise un backoff borné.

Le budget est explicite pour chaque correctif : *coût au repos* (idle) et *coût à
l'action*. Le coût au repos doit rester **strictement nul** partout.

Le lot 0 ci-dessous est entièrement dédié à cet axe : il **retire** des appels qui
existent aujourd'hui.

### 0.3 Sécurité maximale

Deux principes : **fail-closed** sur les chemins de destruction (un doute sur la
santé d'un conteneur ⇒ rollback, jamais suppression), et **aucun secret ni log
brut ne franchit une frontière sortante** (notification, webhook, e-mail).

---

## Lot 0 — Axe performance transverse

Ce lot ne corrige aucun bug fonctionnel : il **supprime du travail inutile** déjà
présent. Il est livré en premier parce qu'il est à risque de régression quasi nul
et qu'il valide la chaîne branche → CI → integration avant les lots sensibles.

Branche : `perf/remove-idle-work`

### 0.1 `waitForVolatileRuntime` — 50 sondes filesystem → 6

[`core/internal/secrets/inline.go:413`](../core/internal/secrets/inline.go#L413)

Aujourd'hui : `time.NewTicker(100 * time.Millisecond)` sur une fenêtre de 5 s,
soit **jusqu'à 50 itérations**. Chaque itération appelle `volatileRuntimeAvailable`,
qui fait `Lstat` + `ReadFile` + `Abs` + un `verifyRuntime` optionnel. Sur un hôte
distant, ce sont **jusqu'à 200 allers-retours SSH** pour une seule activation de
stack chiffrée.

Correctif : backoff exponentiel 100 ms → 200 → 400 → 800 → 1600 → 3200, plafonné à
la même échéance de 5 s. **6 sondes au lieu de 50**, latence pire-cas identique,
latence typique (le tmpfs apparaît en ~200 ms) inchangée.

- Coût au repos : nul avant et après.
- Coût à l'action : **divisé par 8**.
- Régression : aucune, même contrat de sortie (`ready` / `timeout`).

### 0.2 Requête de réconciliation coalescée

[`core/internal/secrets/inline.go:408`](../core/internal/secrets/inline.go#L408)

Aujourd'hui : `requestHostRuntimeReconcile` est appelée **une fois par stack**.
Une assignation globale sur 50 stacks écrit 50 fois le fichier surveillé, donc
déclenche **50 démarrages** de `dockman-secrets-host-reconcile.service` — chacun
relançant un `MaterializeHostRuntime` complet qui reparcourt **toutes** les stacks.
Coût quadratique sur une opération que l'utilisateur perçoit comme unitaire.

Correctif : une seule écriture en fin d'opération de lot. L'API interne passe d'un
appel direct à un accumulateur relâché une fois, à la sortie du handler.

- Coût à l'action : **50 unités systemd → 1**, et *N* parcours complets → 1.
- Régression : aucune, l'état final convergent est identique (la réconciliation
  est idempotente et globale par construction).
- Effet de bord bénéfique : supprime la cause première du §2.3 (rate-limit systemd).

### 0.3 ~~Suppression du `docker rm --force` inconditionnel à chaque build~~ → reporté au lot 3

[`core/internal/docker/compose/command.go:163`](../core/internal/docker/compose/command.go#L163)

**Écarté du lot 0 après tentative.** Le gain visé (un `exec` par build) porte sur
un chemin déclenché par l'utilisateur, pas sur le coût au repos. Or la seule
manière de ne balayer qu'une fois est un état partagé par hôte au niveau du
paquet, ce qui rend **dépendants de l'ordre d'exécution** les quatre tests qui
affirment aujourd'hui cet appel. Un lot dont la raison d'être est « aucune
régression » ne peut pas fragiliser la suite de tests pour économiser un `exec`.

Le vrai défaut de cette ligne n'est de toute façon pas la performance : avec
`BUILDX_CONFIG` isolé et `BUILDX_BUILDER` vide, Dockman **ne peut pas** créer
`buildx_buildkit_default`. Ce conteneur appartient donc à l'utilisateur, et le
supprimer en force détruit son état. C'est une correction de propriété, traitée
au **lot 3 (§3.3)** avec identification par label et mise à jour assumée des
tests concernés.

### 0.4 Restauration du React Compiler sur deux composants

[`ui/src/pages/settings/tab-git.tsx`](../ui/src/pages/settings/tab-git.tsx),
[`ui/src/pages/settings/tab-secrets.tsx`](../ui/src/pages/settings/tab-secrets.tsx)

Un `try/finally` dans le corps d'un composant fait **silencieusement** échouer sa
compilation par le React Compiler. Ces deux composants entiers ne sont donc pas
mémoïsés : chaque frappe clavier y provoque un re-render complet de l'arbre.

Correctif : extraire la logique `try/finally` dans un helper hors composant, ou la
convertir en `.then().catch()` / état explicite. Vérification par le rapport de
compilation en CI.

- Coût au repos UI : **deux sous-arbres re-mémoïsés**.
- Régression : aucune, transformation purement syntaxique à sémantique identique.

**Validation lot 0** : `Fork Checks` (vet, lint, tests Go, tsc, build UI). Aucun
test à écrire — aucun comportement observable ne change.

---

## Lot 1 — Sûreté d'exécution des mises à jour

Le lot le plus urgent : ce code détruit des conteneurs de production sans
supervision. Branche : `fix/update-execution-safety`

### 1.1 Vérification de santé réelle (CRITIQUE)

[`core/internal/docker/updater/updater.go:627`](../core/internal/docker/updater/updater.go#L627)

**État actuel.** `ContainerHealthCheck` lance deux sous-contrôles. Chacun
commence par lire son label (`dockman.update.healthcheck.uptime` ligne 660,
`dockman.update.healthcheck.ping` ligne 715) et **retourne `nil` si le label est
absent**. Sans label — c'est-à-dire pour la quasi-totalité du parc — la fonction
retourne succès immédiatement après `ContainerStart`, sans jamais consulter le
`HEALTHCHECK` natif de l'image ni `State.Health`. Le conteneur précédent est alors
détruit en force (ligne 555) et son image part au nettoyage. **Le rollback
automatique ne se déclenche jamais dans ce cas**, alors qu'il est l'argument de
sûreté central du sous-système.

**Cible.** Un troisième sous-contrôle, `containerHealthCheckRuntime`, **toujours
exécuté** (pas de porte par label), aligné sur la fonction `wait_ready` déjà
présente et éprouvée dans [`protected_update.go:69`](../core/internal/docker/protected_update.go#L69) :

| État observé | Verdict |
|---|---|
| `running` + `health=healthy` | succès |
| `running` + `health=unhealthy` | échec → rollback |
| `die` / `exited` / `dead` | échec → rollback |
| `running`, image **sans** `HEALTHCHECK` | succès après fenêtre de stabilité de 10 s sans `die` |
| `running` + `health=starting` à l'échéance | échec → rollback (fail-closed) |

**Implémentation sans aucun polling.** Le dépôt dispose déjà d'un hub d'événements
Docker mutualisé — [`container/events_hub.go`](../core/internal/docker/container/events_hub.go)
— qui diffuse `health_status`, `die`, `start` et `destroy` sur **un seul**
abonnement daemon par hôte, avec déduplication et reconnexion à backoff. Le
`Service` de l'updater détient déjà `*containerSrv.Service`
([`updater.go:26`](../core/internal/docker/updater/updater.go#L26)) : `SubscribeEvents()`
est directement accessible, **aucune infrastructure nouvelle**.

Séquence : on s'abonne **avant** `ContainerStart` (sinon course sur un conteneur
qui meurt en 50 ms), on démarre, on fait **un** `ContainerInspect` pour lire la
présence d'un `HEALTHCHECK` et rattraper un état déjà terminal, puis on attend sur
le canal d'événements avec **un seul timer**.

- Coût au repos : **nul** (le hub ne tourne que s'il a au moins un abonné, et il
  est partagé avec l'UI qui l'ouvre déjà).
- Coût à l'action : **2 appels `inspect` au total**, contre 0 aujourd'hui et ~90
  qu'aurait coûté une boucle de polling naïve à 1 s.

**Échéance auto-ajustée.** Le délai est lu depuis `Config.Healthcheck.StartPeriod`
de l'inspect : `max(120 s, StartPeriod + 60 s)`, plafonné à 10 min. Un conteneur
avec un `start_period` de 5 min ne sera donc pas déclaré en échec à tort — c'est
la principale régression possible de ce correctif, et elle est neutralisée à la
source plutôt que par un réglage global.

**Non-régression :**
- `verifyHealth == false` ⇒ toute la fonction reste sautée, à l'identique.
- Les deux sous-contrôles par label sont **inchangés**, ligne pour ligne. Ils
  deviennent additionnels, pas alternatifs.
- Un conteneur sain met désormais **+10 s** à être validé s'il n'a pas de
  `HEALTHCHECK`. Borné, unique par mise à jour, et c'est exactement le prix du
  rollback qui ne fonctionnait pas.

### 1.2 Contexte annulé sur le chemin de compensation (CRITIQUE)

[`updater.go:538`](../core/internal/docker/updater/updater.go#L538) et
[`updater.go:548`](../core/internal/docker/updater/updater.go#L548)

Six chemins de compensation utilisent `rollbackContext(ctx)`
(= `context.WithoutCancel` + 1 min). **Deux ne le font pas** et passent `ctx`
directement. Or le cas nominal de déclenchement de ces deux chemins est
précisément l'expiration ou l'annulation de `ctx` — par exemple un healthcheck
qui dépasse son délai. Le `ContainerRemove` de compensation échoue alors
systématiquement, laissant un conteneur `*_updated` orphelin en plus de l'ancien.

Correctif : `rollbackContext(ctx)` sur les deux, alignement strict avec les six
autres. **Une ligne chacune.**

- Coût : nul.
- Régression : aucune — c'est le chemin d'échec qui devient fonctionnel.

### 1.3 Classification des infrastructures sensibles (CRITIQUE)

[`policy.go:245`](../core/internal/docker/updater/policy.go#L245) et
[`update_automation.go:265`](../core/internal/docker/update_automation.go#L265)

**État actuel.** La seule marque `protected` est `dockman.container=true`, soit
Dockman lui-même. Rien ne classe un **socket-proxy**. La transaction de pile
appellerait donc `ContainerRecreate` sur le socket-proxy *à travers* le
socket-proxy : après le `ContainerStop` (ligne 528), Dockman perd son accès au
démon et **même la compensation devient impossible**.

**Cible.** Un conteneur qui monte `/var/run/docker.sock` est classé `protected`,
avec un motif explicite affiché dans l'inventaire. La détection lit
`item.Mounts`, **déjà présent dans le `container.Summary` récupéré** : zéro appel
Docker supplémentaire.

**Ordre de résolution** (important pour la non-régression) :

1. `dockman.container=true` → `protected` (absolu, inchangé)
2. `dockman.update.enable` **présent explicitement** → choix de l'utilisateur, il
   gagne — y compris sur la protection socket
3. montage de `/var/run/docker.sock` → `protected`, motif *« expose le socket
   Docker ; une mise à jour automatique couperait l'accès au démon en cours
   d'opération »*
4. suite inchangée

L'étape 2 avant l'étape 3 est la **soupape** : la restriction est stricte par
défaut et annulable par label, sans toucher au code.

Miroir dans `validateAutomaticTarget` (contrôle au moment de l'exécution, pas
seulement au moment de l'inventaire) : défense en profondeur contre un conteneur
qui aurait changé entre le scan et l'exécution.

- Coût : nul (lecture d'un champ déjà en mémoire).
- Régression assumée et documentée : les conteneurs montant le socket Docker
  sortent de la mise à jour automatique. C'est l'objet même du correctif, le motif
  est affiché dans l'UI, et le label le contourne.

### 1.4 Disjoncteur : distinguer transitoire et destructeur (MAJEUR)

[`execution.go:133`](../core/internal/docker/updater/execution.go#L133)

**État actuel.** Tout `ExecutionFailed` ou `ExecutionRolledBack` crée un
`UpdateExecutionBlock` **permanent** : le conteneur n'est plus jamais retenté
automatiquement. Or `ExecutionFailed` est aussi posé pour des échecs qui n'ont
touché aucun conteneur — un `pull` en erreur 500 côté registre
([`update_automation.go:178`](../core/internal/docker/update_automation.go#L178)),
un verrou d'action non acquis (ligne 227), une erreur de validation préalable
(ligne 160). Une coupure réseau de trente secondes bloque donc définitivement des
conteneurs parfaitement sains.

**Cible.** Classification par « le conteneur a-t-il été touché ? » :

| Situation | État | Bloque ? |
|---|---|---|
| Échec **avant** toute mutation (pull, verrou, validation) | `ExecutionSkipped` | non, retenté au prochain passage |
| Échec **après** mutation (recreate, rollback) | `ExecutionFailed` / `ExecutionRolledBack` | oui, comme aujourd'hui |

- Régression : les éléments désormais `Skipped` seront réessayés à la prochaine
  occurrence planifiée. C'est le comportement voulu, et la fréquence reste bornée
  par `minimumUpdateScanGap` déjà en place.

### 1.5 Suppression du code mort `WithNotifyOnly` (MAJEUR)

[`updater.go:406-419`](../core/internal/docker/updater/updater.go#L406)

Le corps de la branche `NotifyOnlyMode` est **intégralement commenté**, `return`
compris. Un utilisateur qui active « notifier seulement » voit donc ses conteneurs
**mis à jour pour de bon**. Correctif : soit implémenter, soit retirer l'option de
bout en bout. Le plan retient le **retrait** — l'option n'est exposée nulle part
dans l'UI actuelle, et livrer une demi-implémentation sur ce chemin serait pire.

### 1.6 Le test qui empêche tout cela de revenir

`ContainerRecreateWithOptions` — la fonction qui détruit et recrée les conteneurs
de production — **n'a aucun test**, parce qu'il n'existe aucun double du client
Docker dans le dépôt. C'est le point le plus structurant de tout ce plan.

Livrable : une interface minimale extraite des ~8 méthodes réellement utilisées
(`ContainerInspect`, `ContainerCreate`, `ContainerStart`, `ContainerStop`,
`ContainerRemove`, `ContainerRename`, `ImagePull`, `ImageRemove`) et son double en
mémoire, plus les cas :

1. healthcheck expiré par annulation de contexte → l'ancien conteneur redémarre,
   le remplaçant est bien supprimé *(couvre §1.2, échoue sur le code actuel)*
2. image sans `HEALTHCHECK`, conteneur qui meurt à 3 s → rollback *(couvre §1.1,
   échoue sur le code actuel)*
3. image avec `HEALTHCHECK` qui passe `healthy` → bascule et renommage
4. `ContainerRemove` de l'ancien en échec → remplaçant retiré, ancien redémarré
5. conteneur arrêté → swap create-before-remove, reste arrêté

**Validation lot 1** : `Fork Checks` + `Fork Integration Build`. Cahier manuel :
mise à jour d'un conteneur sans HEALTHCHECK qui plante au démarrage (doit
rollback), et vérification qu'un socket-proxy apparaît bien `protected` dans
l'inventaire.

---

## Lot 2 — Intégrité des secrets

Branche : `fix/secrets-integrity`

### 2.1 Désactiver le mode chiffré avec le tmpfs monté détruit tout (CRITIQUE)

[`inline.go:251`](../core/internal/secrets/inline.go#L251)

**État actuel.** `DisableInline` appelle `Materialize` **en premier** (ligne 252),
qui écrit le clair dans `.secrets` — répertoire qui est, à ce moment précis, le
**tmpfs monté** par le runtime hôte. Puis le marqueur est retiré. Le tmpfs reste
monté ; au prochain `cleanupRuntimeMounts` (ou au reboot), il est démonté et
**tous les secrets disparaissent définitivement**. L'utilisateur a demandé « repasse
en clair » et obtient « supprime tout ».

**Cible — ordre inversé et vérifié :**

1. déchiffrer les valeurs **en mémoire**
2. retirer le marqueur `.dockman-sops-inline` (c'est lui qui rend la stack
   « chiffrée » aux yeux de `discoverEncryptedStacks`)
3. demander la réconciliation hôte
4. **attendre que le tmpfs ait effectivement disparu** (backoff borné du §0.1)
5. écrire le clair dans le répertoire redevenu persistant
6. retirer le script de récupération

Si l'étape 4 expire : **abandon, restauration du marqueur, erreur explicite**.
Aucune donnée n'a été touchée à ce stade — c'est la propriété fail-closed.

**Non-régression :** quand il n'y a pas de runtime hôte (`requiresRuntimeFiles`
faux, pas de tmpfs), le chemin actuel est conservé tel quel. C'est le cas le plus
courant et il ne change pas d'un octet.

### 2.2 Requête de réconciliation écrite au mauvais endroit — **reporté, hors lot 2**

[`inline.go:410`](../core/internal/secrets/inline.go#L410)

Le constat est confirmé : `requestHostRuntimeReconcile` écrit à la racine du
`stackFS` de l'alias, alors que l'unité `.path` surveille
`config.StackRoot/.dockman-secrets-reconcile`. Le cas qui casse réellement est
un alias **imbriqué sous** la racine configurée — `StackRoot=/server/stacks`,
alias `media` → `/server/stacks/media` : la stack est bien découverte par le
parcours de l'hôte, mais sa requête atterrit dans un fichier que personne ne
surveille. L'utilisateur voit alors un « en attente » qui ne se résout jamais.

**Pourquoi ce n'est pas corrigé ici.** Écrire au bon endroit suppose que
Dockman connaisse `config.StackRoot`, qui est saisi à l'installation côté hôte
et **n'est aujourd'hui persisté nulle part** côté application. Le corriger
demande de mémoriser cette racine par hôte, de l'exposer au résolveur d'alias,
et de gérer le cas où elle n'a jamais été renseignée. C'est un ajout de
conception, pas une correction — et le deviner produirait un correctif faux
dans la moitié des configurations.

En configuration mono-alias — la racine Compose *est* la racine de stacks — le
comportement actuel est correct. Le sujet est isolé pour un lot dédié.

### 2.3 Rate-limit systemd trivialement atteignable (MAJEUR)

[`host_install.go:96`](../core/internal/secrets/host_install.go#L96)

`StartLimitIntervalSec=10` + `StartLimitBurst=5` sur le service de réconciliation :
**5 déclenchements en 10 secondes** et l'unité passe en échec — définitivement,
entraînant l'unité `.path` avec elle. Activer le chiffrement sur cinq stacks
d'affilée suffit.

Correctif en deux temps :
1. le §0.2 (coalescence) supprime la cause première — une opération de lot ne
   produit plus qu'un seul déclenchement ;
2. la limite est déplacée là où elle a un sens : `TriggerLimitIntervalSec` /
   `TriggerLimitBurst` dans la section `[Path]`, avec une fenêtre réaliste
   (60 s / 20), et le `StartLimit` du service est élargi en conséquence.

### 2.4 La clé privée age peut rester sur l'hôte distant (MAJEUR)

Le nettoyage de la clé transférée n'est pas dans le `trap` : une interruption du
script d'installation laisse une identité age **en clair sur l'hôte distant**.
Correctif : nettoyage inconditionnel dans le `trap EXIT`, avec écrasement avant
suppression.

### 2.5 Isolation des échecs par stack au boot (MAJEUR)

[`host_runtime.go:122-129`](../core/internal/secrets/host_runtime.go#L122)

La boucle de matérialisation **retourne à la première erreur**. Une seule stack
mal formée prive donc **toutes les autres** de leurs secrets au démarrage — ce qui
contredit frontalement l'intention du correctif systemd déjà livré (`Wants=`
plutôt que `Requires=` justement pour que l'échec reste local). Correctif :
accumulation des erreurs, poursuite des stacks saines, `errors.Join` en sortie. Le
code de retour reste non nul, la CI et `systemctl status` restent informatifs.

`discoverEncryptedStacks` ([ligne 164](../core/internal/secrets/host_runtime.go#L164))
reçoit par ailleurs une profondeur maximale et un budget d'entrées : un arbre
pathologique ne doit pas pouvoir retarder le boot.

**Validation lot 2** : tests Go sur `DisableInline` (avec un faux tmpfs), sur
l'isolation par stack et sur le chemin de la requête. Cahier manuel : cycle
complet chiffrer → redémarrer l'hôte → déchiffrer, sur une stack réelle.

---

## Lot 3 — Fuites et exposition

Branche : `fix/leak-exposure`

### 3.1 Le log de build échoué part intégralement en notification (MAJEUR)

[`handler_http.go:80-84`](../core/internal/docker/handler_http.go#L80)

`job.Error` est concaténé sans borne dans le message de notification. Or ce champ
reçoit la sortie stderr de BuildKit en `--progress=plain`, qui contient très
couramment un jeton de dépôt privé, un `_authToken` npm ou une variable de build.
Ce message part ensuite vers un webhook ou un e-mail — **hors de la machine**.

Correctif : la notification ne porte plus que l'identité du job (hôte, image,
Dockerfile, statut) et un renvoi vers l'UI. Le log complet reste consultable
localement, là où il est déjà.

### 3.2 État Buildx dans un `/tmp` prévisible (MAJEUR)

Chemin devinable ⇒ pré-création hostile possible par un utilisateur local.
Correctif : répertoire d'état dédié sous le répertoire de données de Dockman,
permissions `0700`, et `--builder` explicite sur toutes les invocations pour ne
jamais dépendre du builder courant du contexte.

### 3.3 Nettoyage du builder par label (MAJEUR)

[`command.go:163`](../core/internal/docker/compose/command.go#L163) — remonté du
lot 0, voir §0.3. `docker rm --force buildx_buildkit_default` s'exécute à chaque
build sur un conteneur que Dockman **ne peut pas avoir créé** : avec son état
Buildx isolé, il appartient à l'utilisateur. Correctif : identifier les helpers
de Dockman par label et ne jamais forcer la suppression d'un nom deviné. Les
quatre tests qui affirment l'appel actuel sont mis à jour dans le même commit,
puisque c'est la sémantique qui change.

### 3.4 Exclusion dure de la clé age (MAJEUR)

La clé privée age doit être exclue **structurellement** du filtre de synchronisation
Git et de la résolution d'alias, pas seulement par convention de nommage. Une
exclusion par liste noire explicite, testée.

### 3.5 `log.Fatal` sur la migration SMTP héritée (MAJEUR)

[`notifications`](../core/internal/notifications) — une ligne de configuration SMTP
héritée malformée fait **tomber le serveur entier au démarrage**. `log.Fatal` →
`log.Error`, la migration est sautée, le reste démarre.

---

## Lot 4 — Interface

Branche : `fix/ui-hardening`

1. **Sélecteur d'hôte explicite** sur la page Réglages
   ([`tab-secrets.tsx:46`](../ui/src/pages/settings/tab-secrets.tsx#L46)) : `/settings`
   est frère de `:host` dans le routeur, le repli `|| "local"` peut donc viser une
   **autre machine** que celle affichée. Un panneau de secrets qui se trompe
   d'hôte est un incident de sécurité, pas un défaut d'ergonomie.
2. **Confirmation typée** sur l'assignation globale, qui écrase aujourd'hui
   jusqu'à 50 stacks sans aucune demande.
3. **Garde `!loadedPath`** sur le bandeau qui affirme une propriété de sécurité
   (« secrets chiffrés au repos ») alors que rien n'est chargé.
4. **`showWarning` sur `runtimeIssue`** : un échec de matérialisation est
   actuellement annoncé comme un succès.
5. Conversions React Compiler — traitées au §0.4.

---

## Ordonnancement et livraison

| Ordre | Branche | Risque de régression | Pourquoi à cette place |
|---|---|---|---|
| 1 | `perf/remove-idle-work` | très faible | valide la chaîne branche → CI → integration ; ne change aucun comportement observable |
| 2 | `fix/update-execution-safety` | moyen, encadré | urgence maximale ; le double du client Docker sert aussi aux lots suivants |
| 3 | `fix/secrets-integrity` | moyen | dépend de la coalescence livrée au lot 0 |
| 4 | `fix/leak-exposure` | faible | indépendant |
| 5 | `fix/ui-hardening` | faible | indépendant |

Chaque lot : commits atomiques sur sa branche → dispatch `Fork Checks` +
`Fork Integration Build` → **merge sur `integration` seulement au vert** →
cahier de test manuel fourni.

Go, Docker et Node étant absents du Mac, **toute** compilation et **tout** test
passent par GitHub Actions sur `cerede2000/dockman`. Aucun correctif n'est déclaré
terminé sur la foi d'une lecture de code.

---

## Récapitulatif de l'axe performance

Ce que le plan **retire** au total, sans rien ajouter au repos :

| Élément | Avant | Après |
|---|---|---|
| Sondes filesystem par activation de stack chiffrée | jusqu'à 50 | 6 |
| Unités systemd déclenchées par assignation globale (50 stacks) | 50 | 1 |
| Parcours complets de l'arbre de stacks pour cette même opération | 50 | 1 |
| `exec docker rm` par build | 1 systématique | 0 (lot 3) |
| Appels Docker pour vérifier la santé d'une mise à jour | 0 (ne vérifie rien) | 2, puis événementiel |
| Sous-arbres UI non mémoïsés par le React Compiler | 2 | 0 |

Le coût **au repos** reste nul partout : le seul mécanisme d'attente introduit
(§1.1) réutilise un abonnement daemon déjà mutualisé, qui s'éteint avec son
dernier abonné.
