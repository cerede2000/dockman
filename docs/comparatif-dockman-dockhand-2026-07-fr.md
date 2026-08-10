# Comparatif détaillé — Dockman vs Dockhand

**Date** : 29 juillet 2026
**Dockman analysé** : branche `hardening/full-review-remediation`, HEAD `d2b33d8` + modifications non commitées — **analyse au niveau du code source**
**Dockhand analysé** : documentation officielle publiée (`dockhand.pro/manual`, `finsys-dockhand.mintlify.app`, index `llms.txt`, README public) — **analyse au niveau du comportement documenté**

---

## Note de méthode — à lire avant les tableaux

Les deux colonnes **n'ont pas le même niveau de preuve**, et c'est délibéré.

Le dépôt Dockhand porte en tête de son README une consigne explicite adressée aux agents IA leur demandant de ne pas l'ingérer, et le projet est sous **Business Source License 1.1** (bascule en Apache 2.0 au 1er janvier 2029). Son code source n'a donc pas été lu. Deux raisons :

1. Le respect d'une demande explicite du projet.
2. Un risque concret pour Dockman : ce fork prépare des contributions vers `RA341/dockman`, sous licence permissive. Faire transiter des détails d'implémentation issus d'un code BSL vers ces contributions créerait une contamination de licence.

En contrepartie, Dockhand publie une documentation utilisateur nourrie — dont un fichier `llms.txt` explicitement destiné aux modèles de langage — qui décrit le comportement supporté. Pour un comparatif **produit**, c'est une meilleure source que le code : elle décrit ce qui est promis et maintenu, là où la lecture de code produit des affirmations fragiles qui vieillissent mal.

**Convention de notation employée dans tout le document :**

| Marque | Signification |
|---|---|
| **[C]** | Vérifié dans le code source de Dockman, fichier et ligne à l'appui |
| **[D]** | Documenté par Dockhand dans sa documentation officielle |
| **[D?]** | Absent de la documentation Dockhand — non documenté ne veut pas dire inexistant |

Toute case Dockhand marquée **[D?]** doit être lue comme « la documentation officielle ne le mentionne pas », jamais comme « le produit ne le fait pas ».

---

## 1. Synthèse exécutive

**Ce sont deux produits de nature différente, pas deux implémentations concurrentes du même produit.**

**Dockman est un poste de travail d'administration Docker.** Son centre de gravité est l'édition de fichiers Compose : un éditeur Monaco avec sauvegarde optimiste par ETag, bail d'édition, détection de modification externe et résolution de conflit en diff côte à côte ; un arbre de fichiers avec glisser-déposer, recherche floue par WebSocket et vue scindée ; treize onglets d'inspection par conteneur. Git y est un **moyen de transport bidirectionnel** au service de ce flux. Il n'a ni compte utilisateur multiple, ni historique de métriques, ni sauvegarde de volumes.

**Dockhand est une plateforme d'exploitation.** Son centre de gravité est la gestion d'un parc : RBAC, OIDC, LDAP, 2FA, journal d'audit ; métriques persistées avec rétention et endpoint Prometheus ; scanner de vulnérabilités conditionnant les mises à jour ; sous-système de sauvegarde avec restauration inter-environnements ; ordonnanceur unifié ; notifications vers 80+ services. Git y est un **mode de déploiement unidirectionnel** : le dépôt est la source de vérité.

**Conséquence pratique** : sur un homelab mono-utilisateur où l'on édite ses Compose à la main, Dockman offre une expérience d'édition que Dockhand ne cherche pas à couvrir. Sur un parc multi-hôtes exploité par plusieurs personnes, Dockhand couvre des besoins que Dockman n'adresse pas du tout.

### Chiffres de cadrage

| | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Langages | Go 1.26 + React 19 / TypeScript 6 | TypeScript (Node.js 24 en production, Bun en développement) + collecteur Go |
| Framework UI | React 19.2.7 + MUI 9.2 + Vite 8 | SvelteKit 2 / Svelte 5 + shadcn-svelte + TailwindCSS |
| Éditeur de code | Monaco 0.55.1 | CodeMirror 6 |
| Terminal | xterm.js 6.0 | xterm.js |
| Base de données | SQLite uniquement (WAL, CGO), GORM + goose | SQLite (défaut) **ou** PostgreSQL, Drizzle ORM |
| Temps réel | 14 flux Connect-RPC + 4 WebSocket + 1 SSE | SSE (unidirectionnel) + WebSocket (terminaux) |
| Surface d'API | 154 endpoints (80 RPC + 74 routes HTTP) | API REST documentée, périmètre non chiffré |
| Lignes Go hors tests | 32 396 (204 fichiers) | — |
| Lignes TS/TSX hors généré | 27 303 (144 fichiers) | — |
| Couverture de tests | 6 130 lignes, ratio **18,9 %** ; **aucun test frontend** | non documentée |
| Image de base | Alpine 3.24.1 épinglée par digest | OS Wolfi custom construit avec apko, `FROM scratch` |
| Architectures | linux/amd64 + linux/arm64 (runners natifs) | non documenté |
| Licence | Apache 2.0 (amont RA341) | **BSL 1.1** → Apache 2.0 au 01/01/2029 |

---

## 2. Git et GitOps — comparaison approfondie

C'est le domaine où les philosophies divergent le plus nettement.

### 2.1 Modèle conceptuel

| | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Rôle de Git | Transport et filet de sécurité d'un flux piloté par l'interface | Source de vérité d'un déploiement |
| Sens du flux | **Bidirectionnel** | **Unidirectionnel** — « No mechanism for pushing changes back to Git is documented » |
| Unité de liaison | *Folder link* : un dossier de stacks entier ↔ un dossier Git | Un stack ↔ un dépôt + branche + chemin de compose |
| Comparaison | **Trois états** : fichier local, fichier Git, baseline SHA-256 du dernier transfert réussi | `git diff` entre le clone et la version distante |
| Stockage local | Clone « compact » : objets seuls, **sans copie de travail** (`dockman-object-store-v1`) | Clone dans un répertoire du dossier de données, chemins relatifs préservés ; clone superficiel (*shallow*) |

Le modèle à trois états est la différence structurante. Il permet de distinguer « ce fichier a changé côté Git » de « ce fichier a changé des deux côtés » — donc de détecter un vrai conflit plutôt que d'écraser. La documentation de Dockhand **ne traite pas le sort des modifications locales** ; son modèle de tirage suggère qu'elles sont écrasées à la synchronisation, mais aucune politique explicite n'est publiée **[D?]**.

### 2.2 Tableau fonctionnel

| Fonction | Dockman | Dockhand |
|---|---|---|
| Dépôts multiples, publics et privés | Oui **[C]** | Oui **[D]** |
| Authentification HTTPS par token | Oui **[C]** | Oui **[D]** |
| Authentification SSH par clé | Oui, avec passphrase **[C]** | Oui, avec passphrase **[D]** |
| Chiffrement des credentials | AES-256-GCM, AAD liée à l'UUID, clé maître `0600` dans un dossier `0700` **[C]** | AES-256-GCM, clé dans `$DATA_DIR/.encryption_key` **[D]** |
| Test de connexion par credential | Oui, avec dépôt de test optionnel **[C]** | **[D?]** |
| Création d'un dépôt GitHub depuis l'interface | Oui, via l'API GitHub **[C]** | **[D?]** |
| Création de branche manquante | Oui — depuis la branche par défaut ou branche vide orpheline **[C]** | **[D?]** |
| Import Git → Dockman | Oui, avec prévisualisation et sauvegarde préalable **[C]** | Oui, c'est le mode nominal **[D]** |
| Export Dockman → Git avec commit et push | **Oui** **[C]** | **Non documenté** — flux unidirectionnel **[D]** |
| Détection de conflit bidirectionnelle | Oui, baseline à trois voies **[C]** | **[D?]** |
| Diff visuel par fichier | Oui, Monaco côte à côte, sha256 et tailles affichés **[C]** | **[D?]** |
| Résolution de conflit fichier par fichier | Oui, décision indépendante par fichier **[C]** | **[D?]** |
| Non-propagation des suppressions | Oui, par défaut et sans exception **[C]** | **[D?]** |
| Gestion des orphelins | Trois issues : restaurer vers Git, archiver, supprimer — confirmation textuelle `REMOVE LOCAL ORPHAN` **[C]** | **[D?]** |
| Profils de fichiers synchronisés | Trois : Compose seul / Configuration / Tous fichiers réguliers, plus règles d'inclusion et d'exclusion en globs **[C]** | **[D?]** |
| Protection des fichiers sensibles | `.env` réels, `id_rsa`, `*.pem/.key/.p12/.pfx`, noms contenant `secret`/`credential` — exclus par défaut, déblocage par saisie de `INCLUDE SENSITIVE FILES`, jamais mémorisé, **jamais accessible à l'automatisation** **[C]** | Recommandation documentaire de ne pas committer de secrets ; variables stockées chiffrées en base côté Dockhand **[D]** |
| Auto-sync périodique | Oui, 5 min à 24 h, scan uniquement sur nouveau commit **[C]** | Oui, **expressions cron** avec fuseau horaire par planification **[D]** |
| **Webhooks** | **Non** **[C]** | **Oui** — GitHub (HMAC-SHA256 sur `X-Hub-Signature-256`), GitLab (`X-Gitlab-Token`), Gitea, Forgejo ; URL par dépôt et par stack ; filtrage par branche **[D]** |
| Déploiement après synchronisation | Optionnel et explicite, désactivé par défaut **[C]** | Oui, c'est le mode nominal — avec options build, re-pull, force-recreate **[D]** |
| Déploiement conditionnel | Oui, sur nouveau commit **[C]** | Oui, sur `git diff` réel du répertoire compose **[D]** |
| Rollback santé après déploiement | Oui, restauration des fichiers depuis la sauvegarde, attente jusqu'à 60 s **[C]** | **[D?]** |
| Rollback vers un commit antérieur | Oui, sélection par stack et par fichier, comparaison Monaco, sauvegarde de sécurité, mise en pause de l'auto-sync **[C]** | **[D?]** |
| Sauvegardes avant écriture | Oui, systématiques, avec prévisualisation de restauration **[C]** | **[D?]** (le sous-système de sauvegarde général existe, voir §6) |
| Journal d'activité Git | Oui, par folder link **[C]** | Oui, journalisation d'audit des déploiements **[D]** |
| Branche par cible | **Non** — une seule branche par dépôt **[C]** | **Oui** — plusieurs stacks peuvent viser des branches différentes du même dépôt **[D]** |
| Identité et signature des commits | Fixe `Dockman <dockman@localhost>`, non signés **[C]** | sans objet (pas d'écriture) |
| Timeouts sur les opérations réseau | **Aucun** — vérifié, zéro `context.WithTimeout` dans `gitsync` **[C]** | **[D?]** |
| Récupération d'un dépôt divergent | **Ajoutée récemment** — action « Reset to remote » avec confirmation `RESET LOCAL GIT STATE` **[C]** | sans objet |

### 2.3 Lecture

**Dockman domine largement sur la sûreté du transfert.** La baseline à trois voies, la résolution par fichier, la non-propagation des suppressions, les confirmations textuelles sur les opérations destructrices et les sauvegardes systématiques constituent un modèle que la documentation de Dockhand ne décrit pas. C'est cohérent : quand le flux est unidirectionnel et que Git fait autorité, la plupart de ces problèmes n'existent pas — on écrase, c'est le contrat.

**Dockhand domine sur la réactivité et l'exploitation.** Les webhooks signés avec filtrage par branche donnent une boucle de rétroaction en secondes là où Dockman impose cinq minutes minimum. Le cron avec fuseaux horaires est plus expressif qu'un intervalle en minutes. La possibilité de faire pointer plusieurs stacks vers des branches différentes du même dépôt permet un vrai découpage production/préproduction, impossible chez Dockman puisqu'un dépôt n'a qu'une branche.

**Le point aveugle commun** : ni l'un ni l'autre ne documente de mécanisme de **transport chiffré des secrets**. Chez Dockman, la protection des fichiers sensibles est excellente mais son corollaire est qu'un stack restauré sur une machine neuve arrive sans ses `.env`. Chez Dockhand, les secrets vivent chiffrés en base et sont injectés au déploiement — ils ne sont donc pas dans Git non plus, et leur restauration passe par la sauvegarde de la base.

---

## 3. Gestion des conteneurs

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Actions unitaires | start, stop, pause, unpause, restart, remove | start, stop, pause, unpause, restart, remove |
| `kill` / `rename` | **Non exposés** | **[D?]** |
| Actions groupées | start, stop, restart, pause, unpause, update, remove — avec confirmation sur remove | start, stop, restart, pause, unpause, update, remove — avec confirmation |
| Création de conteneur hors Compose | **Non** (uniquement `docker run` en ligne de commande via un bouton dédié) | **Oui** — assistant complet : image, ports, volumes, variables, réseaux, limites, politique de redémarrage, labels |
| Mise à jour d'image | Oui — recréation réelle avec nom temporaire, healthcheck, rollback à chaque étape | Oui — pull, scan optionnel, recréation avec report complet de configuration |
| Politique de mise à jour par vulnérabilité | **Non** | **Oui** — `never`, `any`, `critical_high`, `critical`, `more_than_current` |
| Détection de mise à jour disponible | Oui, comparaison du digest distant | Oui |
| Planification des mises à jour | **Non** — tout le planificateur est du code commenté | **Oui**, via l'ordonnanceur unifié |
| Inspection | 13 onglets : Overview, Logs, Exec, Processes, Networks, Mounts, Environment, Files, Labels, Security, Resources, Health, Inspect JSON | Inspection, métadonnées, navigateur de fichiers |
| Masquage des valeurs sensibles | Oui dans le dialog Monitor (regex `pass|token|secret|credential|private|api.?key|cookie|auth`), **non** dans la page Containers | **[D?]** |
| Colonnes de liste | nom, image, état, santé, uptime, stack, IP, ports, CPU, mémoire, réseau, disque | nom, image, état, santé, uptime, stack, IP, ports, CPU, mémoire, réseau, disque |
| Filtres par état | Oui, tuiles cliquables, **Ctrl+clic pour un filtre additif** | Oui, multi-select |
| Ports cliquables | Oui, IP publique réécrite avec l'adresse du daemon | Oui, via l'IP publique configurée par environnement |
| Domaines Traefik déduits | **Oui** — extraction des règles `Host()`, `HostSNI()`, `HostRegexp()` des labels | **[D?]** |
| Labels de contrôle | `dockman.update`, `dockman.update.disable`, `dockman.container`, healthcheck | Suppression des mises à jour, des notifications, masquage dans l'interface, URL d'accès personnalisée |
| Persistance largeur de colonnes | **Non** | **Oui** |

**Écart notable en faveur de Dockhand** : la création de conteneur hors Compose. Dockman n'en propose aucune — c'est un choix cohérent avec son orientation « tout passe par un fichier Compose versionné », mais c'est une limite réelle pour un usage ponctuel.

**Écart notable en faveur de Dockman** : le dialog d'inspection à treize onglets, en particulier les onglets Security (capabilities, AppArmor, chemins masqués), Resources (ulimits, cgroups) et Health (journal des exécutions du healthcheck), qui n'ont pas d'équivalent documenté.

---

## 4. Stacks Compose et édition

C'est l'écart le plus marqué du comparatif, en faveur de Dockman.

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Modèle de stockage | **Fichiers sur disque**, dans des dossiers d'alias par hôte | **Base de données** (stacks internes) ou dépôt Git (stacks Git) |
| Types de stacks | Fichiers découverts par alias | Trois : Internal (base), Git (dépôt), Untracked (découverts, adoptables) |
| Éditeur | **Monaco 0.55.1** | CodeMirror 6 |
| Coloration syntaxique | Oui, mapping d'extensions maison (yaml, json, ini pour `.env`, shell, etc.) | Oui, YAML |
| Validation Compose | Oui — `docker compose config --quiet` après chaque sauvegarde, widget d'erreurs à ouverture automatique | Oui, validation de syntaxe avec surlignage inline |
| **Sauvegarde automatique** | **Oui**, debounce 500 ms, machine d'état `typing → saving → saved` | **[D?]** |
| **Contrôle de concurrence** | **Oui** — ETag au chargement, `If-Match` à l'écriture, 409 traité comme conflit | **[D?]** |
| **Bail d'édition** | **Oui** — session UUID rafraîchie toutes les 45 s tant que le buffer est sale | **[D?]** |
| **Détection de modification externe** | **Oui** — SSE `/file/events` ; rechargement silencieux si propre, diff si sale | **[D?]** |
| **Résolution de conflit d'édition** | **Oui** — DiffEditor Monaco côte à côte, trois issues (continuer, prendre la version disque, écraser) | **[D?]** |
| **Outline de navigation** | **Oui** — parseur maison tolérant aux fichiers invalides, sections services/networks/volumes/secrets, numéros de ligne, clic pour naviguer | **[D?]** |
| **Vue scindée** | **Oui** — deux éditeurs côte à côte, ouverture par clic molette ou Shift+Enter | **[D?]** |
| **Onglets réordonnables** | **Oui** — glisser-déposer, libellés désambiguïsés à la VS Code sur 3 niveaux de dossiers | **[D?]** |
| **Position du curseur mémorisée** | **Oui**, par onglet, restaurée à la réouverture | **[D?]** |
| Formatage | Oui — Alt+L, appliqué via `executeEdits` pour préserver la pile d'annulation | **[D?]** |
| Arbre de fichiers | Oui — glisser-déposer, upload par dépôt, auto-scroll, menu contextuel, chargement paresseux, mode épinglé, mode compact | sans objet (pas de modèle fichier) |
| **Recherche floue de fichiers** | **Oui** — WebSocket, debounce 200 ms, surlignage caractère par caractère | **[D?]** |
| Templates de fichiers | Oui, avec variables de template saisissables | **Oui** — section Templates au manuel |
| Conversion `docker run` → Compose | **Oui** (composerize intégré) | **[D?]** |
| Visionneuse SQLite intégrée | **Oui** — session streamée pour les fichiers `.db` | **[D?]** |
| Actions de stack | up, down, start, stop, restart, update, redeploy (pull/build/force-recreate) | start, stop, restart, down, déploiement, opérations groupées en parallèle |
| Adoption de stacks existantes | Découverte par label, rattachement au fichier Compose | **Oui** — fonction Adopt explicite convertissant en stack interne |
| Secrets de stack | Fichiers `.env` sur disque, exclus de Git par défaut | **Chiffrés en base**, injectés au déploiement, séparés des variables normales |
| Déploiement distant | Oui, le CLI Compose s'exécute **sur la machine cible** par SSH | Oui, instructions envoyées au daemon distant ; les définitions restent sur le nœud Dockhand |

**Lecture** : Dockman a construit un véritable environnement d'édition — sauvegarde optimiste, bail, détection de conflit externe, outline, split, onglets réordonnables. Rien de tout cela n'apparaît dans la documentation de Dockhand, dont l'éditeur est décrit comme un champ YAML avec coloration et validation.

En revanche, le **modèle de stockage** diverge profondément et chacun a sa logique. Dockman garde les fichiers sur disque, ce qui les rend éditables hors de l'outil et versionnables tels quels — mais impose de gérer la concurrence, d'où toute la machinerie ETag/bail. Dockhand stocke en base, ce qui simplifie la cohérence et permet le chiffrement natif des secrets — mais rend le fichier moins accessible et impose la fonction « Adopt » pour récupérer l'existant.

**La gestion des secrets de stack mérite d'être soulignée** : le modèle Dockhand (chiffrés en base, injectés au déploiement) répond au problème que Dockman n'a pas résolu — comment reconstruire un stack complet et fonctionnel sur une machine neuve. La documentation Dockhand avertit d'ailleurs que les conteneurs redémarrés par Docker perdent les valeurs de secrets sauf référence explicite dans la section `environment:`.

---

## 5. Observabilité

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Métriques conteneurs | CPU, mémoire (working set), réseau, disque, restarts | CPU, mémoire (cache soustrait), réseau, disque |
| Formule CPU | `(ΔcpuTotal/Δsystem) × nCPU × 100`, **plafonnée à `nCPU × 100`** | Formule Docker standard |
| Métriques hôte | CPU, mémoire, nombre de cœurs — lus dans `/proc`, double échantillon à 500 ms | CPU, mémoire, **disque** |
| Cadence | Pilotée par le client, 5 s ; flush 200 ms ; historique **20 points ≈ 100 s en mémoire** | 5 s |
| **Historique persisté** | **Non** — aucune écriture en base, tout est en mémoire | **Oui** — table par environnement, index sur (environnement, horodatage) |
| **Rétention** | sans objet | **30 jours** (métriques hôte), 7 jours (événements), 90 jours (activité), configurables |
| **Endpoint Prometheus** | **Non** — vérifié, aucune occurrence dans le code | **Oui** — `/metrics`, séries étiquetées, intégration Grafana documentée |
| **Alertes à seuils** | **Non** | **Oui** — warning 70 %, alert 85 %, critical 95 % ; action jusqu'au redémarrage automatique |
| Tableau de bord agrégé | Bandeau d'agrégats dans Monitor et Stats | Endpoint dashboard + flux SSE |
| Événements Docker | Oui — hub unique par hôte, whitelist de 13 types, dédoublonnage 5 s, transitions de santé seules | Oui — cycle de vie complet, plus événements stack et utilisateur |
| Journal d'activité | Uniquement pour Git | **Oui**, global, avec rétention 90 jours |
| Notifications | **Non** — le paquet existe mais est du code mort, exclu du build CI car il ne compile pas | **Oui** — SMTP et Apprise (80+ services), filtrage par type d'événement, routage par environnement |

**Écart structurel majeur en faveur de Dockhand.** Dockman est un outil **temps réel sans mémoire** : on voit ce qui se passe maintenant, sur une fenêtre de cent secondes. Il n'y a aucun moyen de répondre à « quelle était la consommation mémoire de ce conteneur hier soir ». Dockhand persiste, expose en Prometheus, et alerte.

C'est le domaine où l'écart est le plus difficile à combler pour Dockman, parce qu'il implique un choix d'architecture — écrire des séries temporelles en SQLite, gérer la rétention, exposer un format d'export — et non l'ajout d'une fonctionnalité.

---

## 6. Sauvegarde et restauration

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Sauvegarde des volumes | **Non** | **Oui** — volumes et montages liés |
| Sauvegarde des configurations | Uniquement les fichiers de stack, dans le cadre de la synchronisation Git | Oui — fichiers Compose et `.env` |
| Sauvegarde des secrets | sans objet (pas de secrets en base) | Via la sauvegarde de la base, avec la clé de chiffrement |
| Destinations | Système de fichiers local, sous `<GIT_STORAGE_PATH>/backups` | **Serveurs REST, stockage S3-compatible**, y compris auto-hébergés |
| Chiffrement des archives | **Non** — mode `0600` et expiration, mais archives **non chiffrées** (l'interface l'indique explicitement) | **Oui** — AES-256-GCM |
| Planification | Rétention par nombre et par âge (30 jours par défaut) | Oui, planifications dédiées |
| Prévisualisation de restauration | **Oui** — action par fichier (`restore`/`remove`/`noop`/`conflict`), seuls les fichiers conformes à l'état post-sauvegarde sont restaurables, sauvegarde de sécurité avant application | **Oui** |
| Restauration inter-environnements | sans objet | **Oui** |
| Récupération après crash | **Non** | **Oui** |

**Lecture** : les deux « sauvegardes » ne recouvrent pas la même chose. Chez Dockman, c'est un **filet de sécurité transactionnel de la synchronisation Git** — on archive les fichiers de stack juste avant de les écraser, pour pouvoir revenir en arrière. Chez Dockhand, c'est un **sous-système de sauvegarde de données applicatives** — volumes compris, vers du stockage distant, chiffré.

Dockman n'a aucune réponse à « mon serveur a brûlé, je veux tout remonter » : ni les volumes, ni les secrets ne sont transportables.

---

## 7. Multi-hôte

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Transports | **Deux** : socket/env local, et **dialer SSH** vers `/var/run/docker.sock` distant | **Quatre** : socket Unix, TCP/TLS avec mTLS, Hawser Standard, Hawser Edge |
| TCP direct avec certificats | **Non exposé dans l'interface** — les champs existent dans le modèle mais ne sont jamais rendus | **Oui**, avec collage de certificats et analyseur d'URL |
| Agent dédié | **Non** | **Oui — Hawser**, agent léger, deux sens de connexion pour traverser NAT et pare-feu |
| Sélection d'hôte | Par **segment d'URL** (`/{host}/...`) | Sélecteur global, rafraîchissement de toutes les pages |
| Test de connexion depuis l'interface | **Non** — la seule action de l'assistant est Enregistrer | **Oui** — version Docker, nombre de conteneurs et d'images retournés |
| Vérification de santé périodique | **Non** — reconnexion **au démarrage uniquement**, 10 tentatives, backoff 2 s → 30 s | **Oui**, avec reconnexion automatique |
| Exécution des commandes Compose | **Sur la machine cible** — session SSH `cd <dir> && docker compose …` | Sur le nœud Dockhand, instructions envoyées au daemon distant |
| Alias de dossiers par hôte | **Oui** — mapping alias → chemin absolu, servant aussi de racine de confinement | sans objet |
| Vérification de clé d'hôte SSH | TOFU maison (première clé acceptée puis épinglée), **pas de `known_hosts`**, pas d'`ssh-agent` | sans objet |
| Clés SSH | **Une seule paire RSA 2048** générée au démarrage, partagée par tous les hôtes, sans import possible | Clés par credential Git |
| Réglages par environnement | Adresse machine, alias | IP publique, collecte d'activité, collecte de métriques, surlignage, labels, icône |

**Écart net en faveur de Dockhand.** L'agent Hawser avec ses deux sens de connexion répond à un vrai problème d'exploitation — piloter un hôte derrière un NAT — que Dockman ne peut pas traiter. L'absence de test de connexion et de vérification de santé périodique dans Dockman est une lacune d'ergonomie plus qu'une limite technique, mais elle se sent à l'usage.

Point notable en faveur de Dockman : le fait que le CLI Compose s'exécute **sur la machine cible** évite la classe de problèmes liée aux chemins de volumes relatifs que la documentation de Dockhand signale explicitement pour les déploiements distants.

---

## 8. Sécurité et authentification

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Authentification | **Désactivée par défaut** (`AUTH_ENABLE=false`) | Activation requise via Settings |
| Modes | Utilisateur/mot de passe local **mono-compte**, OIDC | Utilisateurs locaux multiples, OIDC/SSO, **LDAP/AD**, **2FA** |
| Hachage | bcrypt coût 10 | **Argon2id** |
| Sessions | Cookie, hash SHA-256 non salé en base, 5 sessions max, **`Secure` commenté** | Jetons 32 octets, expiration 30 jours |
| **RBAC** | **Non** | **Oui** — rôles personnalisés, permissions, restriction par environnement |
| **Jetons d'API** | **Non** | **Oui** |
| **Journal d'audit utilisateur** | **Non** | **Oui** |
| Rate limiting | **Aucun** — vérifié, aucun compteur d'échec, aucun verrouillage de compte | **[D?]** |
| Politique d'origine HTTP | Oui, `*` explicitement ignoré ; **une requête sans en-tête `Origin` passe** | **[D?]** |
| CSRF | Pas de jeton — `SameSite=Lax` + contrôle d'origine | **[D?]** |
| En-têtes de sécurité globaux | **Non** — `X-Content-Type-Options` et consorts uniquement sur le sous-routeur Git | **[D?]** |
| Confinement des chemins | `os.OpenRoot` systématique, refus des symlinks, argv-only, allowlists de commandes | **[D?]** |
| Garde d'auto-exécution | `ALLOW_SELF_EXEC` (défaut `false`) sur exec dans le conteneur Dockman, conteneurs de debug privilégiés, shell hôte local | sans objet |
| Scan de vulnérabilités des images | **Non** dans le produit — Trivy uniquement dans la CI, sur l'image publiée | **Oui** — Trivy **et** Grype, par environnement, à la demande ou après pull |
| SBOM, provenance, signature d'image | **Oui** — SBOM, provenance `mode=max`, **Cosign keyless**, gate Trivy bloquant, actions CI épinglées par SHA | **[D?]** |

**Écart majeur en faveur de Dockhand sur l'authentification.** Dockman est structurellement mono-utilisateur : un seul compte, recréé à chaque démarrage depuis la configuration. Il n'y a ni rôles, ni jetons d'API, ni audit. Avec l'authentification désactivée — **le défaut** — toute la surface `/api/protected/**` est ouverte, y compris les credentials Git, le shell hôte et le navigateur de fichiers.

**Écart en faveur de Dockman sur la chaîne de livraison.** SBOM, attestation de provenance, signature Cosign sans clé, gate de vulnérabilités bloquant et épinglage des actions par SHA constituent une posture de supply chain que la documentation de Dockhand ne décrit pas — mais qui n'existe que dans les workflows du **fork**, pas en amont.

---

## 9. Images, volumes, réseaux, nettoyage

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Liste d'images avec usage réel | Oui, croisement `DiskUsage` + listing conteneurs | Oui, groupées par dépôt avec vue par tags |
| Pull d'image | **Pas de RPC dédié** — uniquement via Compose ou l'updater | **Oui**, modale dédiée avec recherche Docker Hub |
| Push d'image | **Non** | **Oui**, vers les registres configurés |
| Tag / untag | **Non** | **Oui** |
| Inspection des couches | Oui — historique, taille par couche, cumul | Oui — modale d'historique |
| Analyse Dive | **Implémentée mais non exposée** — aucun RPC ne l'appelle | **[D?]** |
| Scan de vulnérabilités | **Non** | **Oui** — Trivy/Grype, CVSS, export JSON/CSV |
| Prune d'images | Dangling ou toutes inutilisées | Prune et Prune Unused |
| Création de volume | **Non implémentée** — le RPC renvoie `implement me` | **Oui** |
| Navigateur de volumes | **Oui** — conteneur helper éphémère, sans réseau, `CapDrop: ALL` + 4 capacités, `no-new-privileges` | **Oui** — conteneurs helper avec cache de 30 min |
| Navigateur de conteneurs | **Oui**, avec **repli natif** si l'injection du helper échoue, et détection noexec/tmpfs | Oui — conteneurs **en cours d'exécution uniquement** |
| Édition de fichier en place | **Non** — seul l'upload remplace | **Oui** — éditeur web jusqu'à 1 Mo |
| Création de réseau | **Non implémentée** — le RPC renvoie `implement me` | **Oui** |
| Connexion/déconnexion de réseau | Oui | **[D?]** |
| Nettoyage planifié | Oui — cron, minimum 1 h, 5 cibles, calcul « reclaimable » conservateur corrigeant un biais de Moby 29 | Oui — prune d'images via l'ordonnanceur unifié |
| Registres configurables | **Non** | **Oui** — registres multiples avec credentials |

**Trois RPC non implémentés côté Dockman** (création de volume, création de réseau, et l'analyse Dive non câblée) sont des manques fonctionnels directs, d'autant que les primitives Go existent déjà dans le code.

---

## 10. Terminal, logs et exec

| Fonction | Dockman **[C]** | Dockhand **[D]** |
|---|---|---|
| Terminal conteneur | Oui, PTY Docker | Oui |
| Détection automatique des shells | **Oui** — endpoint dédié, liste les shells réellement présents | **[D?]** |
| Choix de l'utilisateur | **Oui** — contexte conteneur, root, nobody, UID libre | **[D?]** |
| Redimensionnement du terminal conteneur | **Non** — aucune frame de contrôle traitée | **[D?]** |
| **Shell sur l'hôte** | **Oui** — PTY local ou SSH, **dans le dossier du Compose ouvert**, avec redimensionnement | **[D?]** |
| Conteneur de debug | **Oui** — images `nixery.dev/shell/*` pour exec dans un conteneur sans shell | **[D?]** |
| Viewer de logs | **Rendu DOM ligne à ligne avec parseur ANSI maison** | Viewer avec mode suivi |
| Recherche dans les logs | Oui — compteur `n/N`, navigation, surlignage préservant le style ANSI | **[D?]** |
| Mode filtre | Oui — n'afficher que les lignes correspondantes | **[D?]** |
| Filtre par flux | Oui — stdout / stderr séparés | **[D?]** |
| Plage temporelle | Oui — bornes `From`/`To`, la borne haute arrête le suivi | **[D?]** |
| Logs multi-conteneurs fusionnés | **Oui** — tri par horodatage, chips par conteneur, palette de 10 couleurs | **[D?]** |
| Reprise sans doublon | **Oui** — filigrane par conteneur, barre de rejeu figée éliminant le recouvrement | **[D?]** |
| Buffer | 2 000 entrées, flush 80 ms, backoff 1 s → 30 s | **[D?]** |
| Téléchargement des logs | Oui, `.txt` | **[D?]** |
| Panneau flottant | **Oui** — barre de 26 px, ouverture au survol, délai de grâce 500 ms | **[D?]** |

**Écart net en faveur de Dockman**, et c'est cohérent avec son positionnement : le viewer de logs est un composant sophistiqué (1 285 lignes) avec des raffinements — continuité ANSI inter-lignes, reprise par filigrane, fusion multi-conteneurs triée par horodatage — que la documentation de Dockhand ne mentionne pas.

Le **shell sur l'hôte** est une capacité que Dockhand ne documente pas du tout, et qui est particulièrement utile en homelab.

---

## 11. Qualité et maturité du code (Dockman uniquement)

Cette section ne peut pas être comparative. Elle est fournie pour l'auto-évaluation.

| Indicateur | Valeur |
|---|---|
| Fichiers Go hors généré | 204 (159 sources / 45 tests) |
| Lignes source / test | 32 396 / 6 130 — **ratio 18,9 %** |
| Package le mieux couvert | `gitsync` — 33 %, 14 fichiers de test, **66 % de toutes les lignes de test du projet** |
| Packages ≥ 200 lignes **sans aucun test** | 11, dont `dockyaml`, `viewer`, `debug`, `desktop`, `lsp`, `notifications`, `argos` |
| **Tests frontend** | **Aucun** — ni Vitest, ni Jest, ni Playwright, zéro fichier de test dans `ui/` |
| Code mort identifié | **≥ 2 400 lignes** + 2 packages exclus de la CI |

**Code mort recensé** : `compose_old.go` (462 lignes intégralement commentées), le planificateur de l'updater (90 lignes commentées), `cleaner/scheduler.go` (95 lignes sans appelant), `cmd/exp` (305 lignes sans `main()`), `cmd/updater` (`main()` vide), `ssh/sftp.go`, `pkg/cache`, `tab-updater.tsx` (208 lignes commentées), `docker-table.tsx` (94 lignes commentées), `inspect-tab-files.tsx`. Les packages `notifications` et `lsp` sont explicitement exclus du build par la CI — `notifications` **ne compile même pas**.

### Deux erreurs de compilation dans l'arbre de travail — à corriger

Vérifiées personnellement, elles proviennent de modifications non commitées et **empêchent le package `docker` de compiler** :

1. **`core/internal/docker/handler_containers.go:274`** appelle `compose.TryLockStack(dkSrv.Host, key)`, mais le bloc d'import du fichier ne contient pas `internal/docker/compose` — seul `github.com/docker/compose/v5/pkg/api` y figure. En Go les imports sont par fichier : l'identifiant `compose` n'existe pas dans ce fichier.
2. **`core/internal/docker/file_browser_http.go:142`** référence une variable `directory` qui n'est déclarée nulle part dans `containerFilesAction` — la variable locale s'appelle `requested`.

---

## 12. Verdict par domaine

| Domaine | Avantage | Ampleur |
|---|---|---|
| Édition de fichiers Compose | **Dockman** | Très net — aucun équivalent documenté |
| Viewer de logs | **Dockman** | Très net |
| Sûreté des transferts Git | **Dockman** | Très net |
| Inspection de conteneur | **Dockman** | Net |
| Shell hôte et exec avancé | **Dockman** | Net |
| Chaîne de livraison (SBOM, signature) | **Dockman** *(fork seulement)* | Net |
| Nettoyage et calcul du récupérable | **Dockman** | Léger |
| Réactivité Git (webhooks) | **Dockhand** | Net |
| Branches par cible | **Dockhand** | Net |
| Authentification et RBAC | **Dockhand** | Très net |
| Historique de métriques et Prometheus | **Dockhand** | Très net |
| Sauvegarde et restauration de données | **Dockhand** | Très net |
| Scan de vulnérabilités | **Dockhand** | Très net |
| Notifications | **Dockhand** | Très net |
| Ordonnancement général | **Dockhand** | Net |
| Multi-hôte (agent, NAT, santé) | **Dockhand** | Net |
| Création de ressources (conteneur, volume, réseau) | **Dockhand** | Net |
| Gestion des registres et images (push, tag) | **Dockhand** | Net |

### Ce que ça dit

Le partage est net et il est **structurel, pas accidentel** : Dockman gagne partout où il s'agit de **manipuler finement une configuration**, Dockhand gagne partout où il s'agit d'**exploiter un parc dans la durée**.

Les avantages de Dockman sont concentrés sur des composants profonds et difficiles à répliquer — l'éditeur avec sa gestion de concurrence, le viewer de logs avec sa reprise par filigrane, le modèle de baseline à trois voies. Ce sont des mois de travail spécialisé.

Les avantages de Dockhand sont concentrés sur des **sous-systèmes entiers absents** chez Dockman — persistance de métriques, sauvegarde de volumes, RBAC, notifications, scan CVE. Chacun est un chantier d'architecture, pas une fonctionnalité.

Aucun des deux ne rattrapera l'autre par petites touches. Ils occupent des positions différentes, et pour un usage homelab mono-utilisateur centré sur l'édition de Compose versionnés, le choix de Dockman est défendable sur ses propres mérites — à condition d'accepter l'absence d'historique, de sauvegarde de volumes et de gestion multi-utilisateurs.

---

## Sources

**Dockman** : lecture directe du code source à `d2b33d8` + arbre de travail, dépôt local `/Users/benjy/Documents/dockman`.

**Dockhand** : [documentation officielle](https://finsys-dockhand.mintlify.app/) (index `llms.txt`), [manuel utilisateur](https://dockhand.pro/manual/), [site produit](https://dockhand.pro/), [dépôt public](https://github.com/Finsys/dockhand) (page de présentation uniquement — README, licence, structure ; le code source n'a pas été consulté).

Pages de documentation Dockhand exploitées : Git Integration, Git Webhooks, Architecture, License, Stacks, Containers, Images, Monitoring &amp; Metrics, Environments, Scheduling &amp; Automation, Notifications, File Browser, et la table des matières complète du manuel utilisateur.
