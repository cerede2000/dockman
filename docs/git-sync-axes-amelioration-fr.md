# Synchronisation Git — axes d'amélioration par ordre de priorité

**Date** : 28 juillet 2026
**Base analysée** : `hardening/dependencies-git-preview-performance` @ `700e801`
**Objet** : feuille de route resserrée pour la partie git. Ne contient que des chantiers que je recommande réellement.

---

## Position de départ

La partie git est **très bonne sur son axe : la sûreté des transferts**, et devant Dockhand sur ce terrain.

Le point à garder en tête pour lire cette feuille de route : Dockman ne traite pas git comme la source de vérité, mais comme un **moyen de transport et un filet de sécurité** au service d'un flux piloté par l'interface. La baseline en est la preuve — on compare trois états (fichier local, fichier git, empreinte du dernier transfert réussi) au lieu de deux. Dockhand fait le choix inverse : dépôt = vérité, on tire et on déploie.

Ce choix donne des capacités que la concurrence n'a pas — bidirectionnalité, conflits détectés dans les deux sens avec résolution fichier par fichier, non-suppression par défaut, sauvegarde obligatoire avant écriture, jeton de prévisualisation revalidé sous verrou, protection des fichiers sensibles avec confirmation typée jamais mémorisée. Il crée en revanche une classe de problèmes qu'un modèle unidirectionnel n'a pas, et c'est là que se concentrent les trois premiers chantiers.

**Ce qui sépare l'état actuel d'un « excellent » n'est pas une liste de fonctionnalités, mais trois correctifs de robustesse et le transport des secrets.**

---

## Chantier 1 — Timeouts réseau et ordonnanceur

**Effort** : court · **Gain** : élimine le mode de panne le plus probable

### Pourquoi

Aucune opération réseau n'est bornée dans le temps. Vérification : `grep -c "context.WithTimeout" core/internal/gitsync/*.go` retourne **0**. Le fetch, le pull et le push utilisent le contexte du serveur, sans échéance ; le client HTTP à 20 secondes de `service.go:116-121` ne couvre que l'API GitHub, pas le transport go-git.

Couplé à un ordonnanceur **mono-goroutine séquentiel** (`automation.go:188-201` et `232-250`), l'effet est disproportionné : une connexion TCP à moitié morte — VPN qui tombe, NAT qui expire, dépôt auto-hébergé qui redémarre — bloque l'opération indéfiniment, et **plus aucun binding n'est synchronisé**. Les verrous `automation:<id>` et dépôt restent tenus, donc toute action manuelle répond « déjà en cours ». Seul un redémarrage de Dockman rétablit la situation.

Un outil de synchronisation qui peut se figer sans porte de sortie est un problème plus grave que n'importe quelle fonctionnalité absente. C'est pour cela que ce chantier passe avant tout le reste.

### Quoi faire

- Encadrer chaque opération réseau d'un `context.WithTimeout` (2 à 5 minutes, configurable) dans `runDueAutoSyncs` et `fetchRepositoryLocked`, ainsi que sur tous les `PushContext` et `ListContext`.
- Sur expiration : libérer les verrous, marquer le binding en erreur avec un message explicite, **passer au suivant** au lieu d'interrompre le cycle.
- Recharger le binding **après** acquisition du verrou (`automation.go:266` puis `277`) : aujourd'hui il est lu avant, donc une modification concurrente de la sélection Compose peut faire partir l'exécution sur un état périmé, et synchroniser une stack tout juste désélectionnée.

### Fait quand

Débrancher le réseau pendant un cycle de synchronisation automatique : le binding concerné passe en erreur dans le délai imparti, les autres continuent d'être traités, et une action manuelle reste possible sans redémarrage.

---

## Chantier 2 — Récupération d'un dépôt divergent

**Effort** : court · **Gain** : supprime le seul cul-de-sac fonctionnel du système

### Pourquoi

`ExportBinding` commite sur la branche partagée **puis** pousse (`binding.go:1033-1041`, même schéma dans `local_deletion.go:291-301` et `460-470`). Si un écrivain externe pousse entre le fetch et le push, le push échoue mais **le commit local subsiste** : le dépôt bascule en « diverged ». À partir de là :

- le pull est refusé — *« local and remote history require an explicit conflict decision »* (`repository.go:578-580`) ;
- le push est refusé — *« remote contains commits that are not present locally »* (`repository.go:620-622`) ;
- l'automatisation passe en `blocked`.

Aucune route de sortie n'existe : vérification faite, aucune occurrence de `reset`, `force-push` ou `discard` dans `handler_http.go`. Et `DeleteRepository` est refusé tant que des bindings existent. La seule issue actuelle est de tout délier, supprimer le dépôt, le recréer — et **perdre toutes les baselines**, ce qui repasse chaque fichier en conflit `no_baseline`.

### Quoi faire

- Ajouter une opération « réinitialiser la branche locale sur origin », derrière confirmation textuelle et journalisée dans l'activité. Elle est **sans danger ici** : le stockage est compact, sans copie de travail, donc les fichiers des stacks ne sont jamais touchés — seule la référence locale bouge.
- Exposer clairement l'état dans l'interface : nombre de commits en avance et en retard, et le bouton de résolution à côté.
- Conserver les baselines à travers l'opération : c'est tout l'intérêt par rapport au contournement actuel.

### Fait quand

Provoquer une divergence (pousser un commit externe entre un fetch et un push), puis rétablir une synchronisation normale depuis l'interface, sans délier ni recréer, et sans qu'aucun fichier ne repasse en conflit.

---

## Chantier 3 — Bug de sélection racine

**Effort** : court · **Gain** : rétablit une garantie que l'interface promet déjà

### Pourquoi

Pour un Compose situé à la racine du binding, `filepath.Dir` produit `"."`, qui atterrit dans `policy.selectedRoots` (`binding.go:2126-2129`). Or `selectsPath` (`binding.go:2191-2205`) teste :

```go
if root == "." || relative == root || strings.HasPrefix(relative, root+"/") {
    return true, true
}
```

La branche `root == "."` renvoie **vrai pour tout chemin**. Conséquence : avec un stack racine sélectionné, les stacks imbriquées explicitement désélectionnées — ou mises en pause « récupération » — restent dans l'inventaire ; un export du stack racine **pousse aussi leurs fichiers vers git**, et l'import automatique peut écrire chez elles.

Les conflits limitent la casse, mais la portée promise à l'écran est violée. La sémantique inverse est pourtant assumée ailleurs dans le même fichier (`binding.go:1421` : *« A root stack owns root files only »*). Les tests ne couvrent que la topologie sans Compose racine.

C'est le genre de défaut qui coûte cher en confiance : un utilisateur qui découvre que ses stacks désélectionnées sont quand même parties sur git cesse de faire confiance à tout le reste du système, y compris à ce qui fonctionne très bien.

### Quoi faire

- Pour la racine `"."`, ne sélectionner que les fichiers **sans `/`** dans leur chemin relatif.
- Ne renvoyer `traverse` que pour atteindre les sous-racines effectivement sélectionnées.
- Ajouter le cas de test manquant : Compose racine **plus** stacks imbriquées désélectionnées.

### Fait quand

Un binding avec Compose racine et deux stacks imbriquées désélectionnées : l'export ne transporte que les fichiers de la racine, et les stacks désélectionnées n'apparaissent pas dans la prévisualisation.

---

## Chantier 4 — Transport des secrets

**Effort** : important · **Gain** : comble le seul vrai manque fonctionnel, et personne d'autre ne le fait

### Pourquoi

`isSensitivePath` (`binding.go:2536-2546`) classe comme sensibles les `.env` réels — les gabarits `.env.example`, `.sample`, `.template`, `.dist` restent autorisés —, `id_rsa`, `id_ed25519`, les extensions `.pem`, `.key`, `.p12`, `.pfx`, et tout nom contenant `secret` ou `credential`. Ces fichiers sont exclus par défaut, débloquables uniquement en tapant `INCLUDE SENSITIVE FILES`, sans mémorisation — et l'automatisation ne passe **jamais** ce drapeau (vérifié : aucune occurrence d'`IncludeSensitive` dans `automation.go`).

C'est une excellente protection contre la fuite de secrets sur un dépôt distant. Son corollaire est qu'un stack restauré depuis git sur une machine neuve arrive **sans ses `.env`** : on récupère la forme, pas le contenu. Le scénario « mon serveur a brûlé » n'est donc couvert qu'à moitié.

Aucun mécanisme de transport chiffré n'existe : ni SOPS, ni age, ni coffre synchronisable. Dockhand ne résout pas ce problème non plus — c'est donc autant un manque à combler qu'un différenciateur à prendre.

### Quoi faire

- Intégrer un chiffrement de fichiers **age** ou **SOPS**, avec la clé stockée dans le vault existant — AES-256-GCM, données associées liées à l'UUID, clé maître en `0600` dans un dossier `0700` : l'infrastructure de sécurité est déjà là et de bonne facture.
- Nouvelle catégorie de transfert : « secret chiffré », distincte de « sensible en clair ». Un `.env` chiffré devient synchronisable **et** automatisable, puisque le contenu poussé est illisible sans la clé.
- La clé maître n'entre jamais dans le dépôt. Prévoir explicitement son export et sa réimportation — c'est le point critique de la reprise après sinistre, et il doit être documenté pas à pas.
- Restitution au moment de l'import : déchiffrement vers le fichier local, avec la même sauvegarde préalable que tout autre écriture.

### Fait quand

Reconstruire un stack complet **et fonctionnel** — secrets compris — depuis un dépôt distant sur une instance Dockman neuve, à partir de la seule clé maître.

---

## Chantier 5 — Webhook signé

**Effort** : moyen · **Gain** : rattrape le principal écart d'expérience avec Dockhand

### Pourquoi

La synchronisation est exclusivement en mode « tirage », avec un intervalle minimal de cinq minutes. Pousser un commit depuis son poste et attendre sans savoir quand ça partira est la différence d'usage la plus visible avec Dockhand, qui propose des webhooks signés.

Ce chantier vient **après** les trois premiers volontairement : un webhook augmente la fréquence de déclenchement, donc la probabilité de rencontrer les blocages des chantiers 1 et 2. Le construire avant reviendrait à accélérer une machine dont les freins ne sont pas réparés.

### Quoi faire

- Point d'entrée HTTP par dépôt, avec secret partagé et **vérification de signature** (`X-Hub-Signature-256` pour GitHub, jeton pour GitLab).
- Anti-rejeu : horodatage plus identifiant de livraison mémorisé sur une courte fenêtre.
- Filtrage par branche : ignorer tout ce qui ne concerne pas la branche du dépôt.
- **Mise en file d'attente par stack**, avec fusion des déclenchements rapprochés — sans cela, une rafale de commits provoque des exécutions concurrentes sur le même binding.
- Le webhook déclenche exactement le même cycle que l'automatisation périodique, sans chemin de code parallèle : une seule logique à maintenir et à tester.

### Fait quand

Un `git push` déclenche la synchronisation en quelques secondes ; une rafale de cinq commits produit une seule exécution ; une requête avec une signature invalide ou rejouée est rejetée et journalisée.

---

## Chantier 6 — Branche par binding

**Effort** : moyen · **Gain** : traite la cause racine de la divergence et débloque les environnements

### Pourquoi

Un dépôt n'a qu'un `DefaultBranch` (`models.go:34`), tous ses bindings le partagent, et l'export commite directement dessus. Trois conséquences :

- pas d'isolation par environnement — un hôte « prod » et un hôte « test » ne peuvent pas suivre deux branches du même dépôt (le contournement, déclarer deux dépôts sur la même URL distante, fonctionne mais reste peu élégant) ;
- pas de flux de revue possible ;
- surtout, **écrire directement sur une branche partagée depuis plusieurs instances fabrique la divergence**. Le chantier 2 en soigne le symptôme ; celui-ci en réduit la cause.

### Quoi faire

- Champ « branche » au niveau du binding, avec repli sur la branche par défaut du dépôt.
- Création de la branche à la volée si elle n'existe pas, sur le modèle de `createEmptyRemoteBranch` déjà présent.
- Les baselines sont déjà indexées par binding : aucune migration de fond n'est nécessaire.

### Fait quand

Deux hôtes liés au même dépôt sur deux branches distinctes se synchronisent sans jamais interférer.

---

## Chantier 7 — Traçabilité des commits

**Effort** : court · **Gain** : attribution, et cohérence avec la posture du projet

### Pourquoi

Tous les commits sont écrits par `Dockman <dockman@localhost>` (`repository.go:376-377`), sans signature. Dans un historique partagé entre plusieurs instances ou plusieurs personnes, aucun changement n'est attribuable.

Il y a par ailleurs une incohérence de posture : le projet signe ses images Docker avec cosign sans clé, publie SBOM et provenance, bloque les vulnérabilités corrigeables au build — mais ne signe pas ses propres commits et ne permet pas de configurer une identité.

### Quoi faire

- Identité d'auteur configurable par dépôt — nom et adresse — avec le comportement actuel comme valeur par défaut.
- Ajouter l'origine dans le message de commit : instance, hôte, binding.
- Option de signature SSH des commits, réutilisant une clé du vault. La signature SSH est nettement plus simple à opérer que GPG et suffit largement pour l'usage visé.

### Fait quand

Un historique de dépôt permet de dire quelle instance et quel binding sont à l'origine de chaque commit.

---

## Chantier 8 — Lot d'hygiène

**Effort** : court · **Gain** : dette technique et faux signaux à l'écran

À traiter groupé, quand l'occasion se présente :

- **`automation.go:479`** — le chemin d'erreur utilise `updateActiveStackStatuses` (non préservante) là où le reste du fichier utilise `updateActiveStackStatusesPreservingLocal` (lignes 292, 294, 511, 513). Un échec réseau écrase donc les états `locally_deleted` et `orphaned`, et au cycle suivant tout est repeint `up_to_date` : **l'invite « restaurer / supprimer » disparaît**, laissant croire que tout est réglé. Pas de perte de données, mais un faux signal sur une décision destructrice.
- **`automation.go:335-336` et `440-442`** — `retryCurrentDeployment` inclut l'état `partial`, mais la réinjection des cibles ne couvre que `failed|pending` : les stacks échouées d'un lot mixte ne sont jamais redéployées par « Lancer maintenant », contrairement à ce qu'annonce le commentaire.
- **`provision.go:347-361`** — les dossiers `.dockman-provision-staging-<uuid>` survivent à un crash en plein déploiement, invisibles puisque masqués de la synchronisation. `cleanupStaleTemporaryWorktrees` ne couvre que les `.dockman-export-`. Ajouter un balayage équivalent au démarrage.
- **`repository.go:879`** — `authForRepository` rappelle `githubHostKeys` à **chaque** fetch et push SSH : une panne ou une limitation de débit de l'API GitHub casse la synchronisation alors que le serveur git est joignable. Un cache avec durée de vie suffit.
- **Code mort** — le paquet `core/internal/git/` (611 lignes dans `service.go`, plus deux handlers) n'est importé nulle part, le migrator qui l'utilisait est commenté (`app.go:136-139`), et il contient des écritures **non confinées** (`SyncFile`, `service.go:374-376` : `filepath.Join` sans validation). À supprimer avant réutilisation accidentelle. Le proto `spec/protos/git/v1/git.proto` (5 RPC) n'est enregistré nulle part côté Go. Idem pour `pruneBindingBackups` (`binding.go:3156`), sans appelant de production.
- **`handler_http.go:464`** — `binding, _ := GetBinding` avale l'erreur : l'activité est enregistrée avec des identifiants vides si le binding n'existe pas.

---

## Écarté volontairement

Trois directions ne figurent pas dans cette feuille de route, et c'est un choix assumé :

- **RBAC, SSO/OIDC et journal d'audit utilisateur** — c'est l'axe « plateforme d'entreprise » où Dockhand est devant. Le coût est élevé et le bénéfice faible pour un usage homelab mono-utilisateur. À reconsidérer seulement si Dockman devient réellement multi-utilisateurs.
- **Réécriture ou nettoyage automatique de l'historique git** — risque disproportionné par rapport au besoin. À ne pas confondre avec un simple nettoyage du clone local, qui existe déjà via la compaction.
- **Déploiement automatique par défaut** — le choix actuel (synchroniser sans déployer, déploiement contrôlé en option explicite) est le bon, et c'est un avantage de sûreté sur Dockhand. À conserver tel quel.

---

## Récapitulatif

| # | Chantier | Effort | Nature |
|---|---|---|---|
| 1 | Timeouts réseau et ordonnanceur | court | robustesse |
| 2 | Récupération d'un dépôt divergent | court | robustesse |
| 3 | Bug de sélection racine | court | correction |
| 4 | Transport des secrets | important | fonctionnel |
| 5 | Webhook signé | moyen | fonctionnel |
| 6 | Branche par binding | moyen | conception |
| 7 | Traçabilité des commits | court | conception |
| 8 | Lot d'hygiène | court | dette |

Les trois premiers forment un bloc à traiter ensemble : tant qu'ils ne sont pas faits, chaque nouvelle fonctionnalité élargit la surface d'un système qui peut se bloquer.
