# Revue globale Dockman — 2026-08-07

44 290 lignes de Go, 25 paquets, plus l'interface React.

## Ce que couvre cette revue, et pourquoi

La revue du 2026-08 (matinée) a traité en profondeur **cinq domaines** : secrets, mises
à jour automatiques, notifications, builds Buildx, git récent. Ses 4 critiques et la
plupart de ses majeurs ont été corrigés dans la foulée — huit branches, toutes mergées
sur `integration`, CI verte à chaque étape.

Cette revue-ci porte sur **la surface qui n'avait jamais été auditée** —
authentification, système de fichiers, SSH, WebSocket, serveur HTTP, couverture de
tests — et surtout sur un **axe transverse jamais exploré : le multi-hôte**.

Elle ne re-lit pas ligne à ligne les 44 000 lignes. Ce serait une dépense sans
rendement : les domaines déjà audités l'ont été il y a quelques heures, et leurs
correctifs sont couverts par des tests qui échouent sur l'ancien comportement, vérifié
un par un. L'effort est allé là où il produit de l'information neuve — et c'est
l'axe multi-hôte qui a rendu le critique du §1.

---

## 0. Le modèle multi-hôte

Comprendre ce modèle est ce qui a permis de trouver le §1.0, donc il vaut d'être posé.

**Un registre unique, `activeClients`**, associe un nom d'hôte à une entrée portant son
client Docker, son client SSH et **sa propre table d'alias**. `GetDockerService(name)`
construit un `docker.Service` **par requête**, dont toutes les fermetures capturent
cette entrée : le résolveur de chemins de compose, la table d'alias, le fournisseur
d'environnement de secrets. Un service ne peut donc pas voir les alias d'un autre hôte
— l'isolation est structurelle, pas déclarative.

**Les caches partagés sont correctement clés.** Les deux états de paquet qui
survivent aux requêtes — le hub d'événements Docker et le cache de statistiques —
sont indexés par `*client.Client`, soit un par hôte connecté, et `ReleaseClientState`
les purge à la déconnexion. Le verrou d'action de pile est indexé par
`hôte + "\x00" + fichier`. Aucune collision inter-hôtes possible sur ces trois-là.

**Les opérations local-only sont gardées explicitement** : shell hôte, redémarrage,
auto-mise-à-jour et mise à jour protégée refusent un hôte distant avec un message
clair. C'est fait quatre fois de suite dans `handler_http.go`, correctement.

**Ce que cet axe a révélé :** la garde d'auto-mise-à-jour est conditionnée à
`u.hostname == LocalClient` sur un chemin, absente sur un autre. C'est en cherchant
« qu'est-ce qui suppose d'être local ? » que le trou du §1.0 est apparu.

---

## 1. Mise à jour — un critique trouvé par l'axe multi-hôte

### 1.0 [CRITIQUE] Le bouton Update du Monitor peut détruire Dockman lui-même

**Corrigé** — branche `fix/manual-update-self-guard`.

Trois chemins peuvent mettre un conteneur à jour, et **deux seulement** vérifiaient
le label `dockman.container` :

| Chemin | Garde `dockman.container` |
|---|---|
| `containersUpdateLoop` | oui ([`updater.go:399`](../core/internal/docker/updater/updater.go#L399)) |
| Exécution automatique | oui (`validateAutomaticTarget`) |
| **`ContainersForceUpdate`** — le bouton Update du Monitor | **non** |

L'interface ne le gardait pas non plus. Recréer Dockman via l'API Docker ne peut pas
fonctionner : le processus meurt sur son propre `ContainerStop`, **avant** que le
remplaçant soit créé. Résultat : pas de remplaçant, pas de rollback, un conteneur
arrêté et un `docker start` manuel sur l'hôte pour revenir.

La garde socket ajoutée plus tôt ne couvre pas ce cas : elle attrape un conteneur qui
**monte** le socket, et un Dockman qui joint son démon par un socket-proxy ne monte
rien. C'est précisément le déploiement où le bouton était atteignable.

Aucun label ne contourne cette garde — il n'existe aucune façon sûre de le faire par
l'API.

### 1.1 [MINEUR] Incohérence sur les hôtes distants

`updater.go:399` conditionne la garde à `u.hostname == LocalClient`, alors que
l'inventaire marque `protected` quel que soit l'hôte. Un Dockman tournant sur un hôte
distant est donc affiché protégé mais serait mis à jour par la boucle. Le
comportement est défendable — ce n'est pas *ce* Dockman, la connexion n'est pas
coupée — mais l'affichage et le code disent deux choses différentes.

---

## 2. Authentification

C'est le seul domaine où je remonte des constats de niveau majeur.

### Ce qui est bien fait, et qui n'est pas courant

- **Jeton de session** : 32 caractères sur un alphabet de 62 via `crypto/rand`, soit
  ≈ **190 bits d'entropie**. Très au-dessus du nécessaire.
- **Stockage haché** : la base ne contient qu'un SHA-256 du jeton
  ([`service.go:117`](../core/internal/auth/service.go#L117)). Une fuite de base ne
  donne aucune session utilisable. Beaucoup de projets bien plus gros stockent le
  jeton en clair.
- **Mots de passe** : bcrypt au coût par défaut, comparaison par
  `CompareHashAndPassword`.
- **Middleware fail-closed**, et le bug de contexte (`r.WithContext` dont le retour
  était jeté) est déjà corrigé.

### 2.1 [MAJEUR] Le cookie de session n'a pas l'attribut `Secure`

[`auth/utils.go:42`](../core/internal/auth/utils.go#L42)

```go
HttpOnly: true,
SameSite: http.SameSiteLaxMode,
// Secure attribute (send only over HTTPS) - for production
// Secure:   true,
```

L'attribut est **commenté**. Or Dockman sert nativement en HTTPS dès que des
certificats sont configurés ([`server.go:66`](../core/internal/app/server.go#L66)).
Un déploiement TLS émet donc un jeton de session que le navigateur acceptera de
renvoyer en clair : toute requête HTTP vers le même hôte, tout downgrade, toute
redirection mal configurée l'expose.

**L'asymétrie est ce qui frappe** : le cookie d'état OIDC, qui vit 300 secondes et ne
donne accès à rien, reçoit un `Secure` piloté par la configuration. Le jeton de
session, qui est *la* clé de l'application, n'en a aucun.

**Correction.** Poser `Secure` conditionnellement plutôt qu'inconditionnellement — un
`Secure: true` en dur casserait tous les accès HTTP en LAN, qui sont le cas d'usage
majoritaire d'un homelab. La règle juste : `Secure` quand la requête est arrivée en
TLS (`r.TLS != nil`) ou derrière un proxy de confiance annonçant
`X-Forwarded-Proto: https`. `createCookie` doit donc recevoir la requête.

### 2.2 [MAJEUR] Le drapeau `AUTH_OIDC_SECURE` est documenté à l'envers

[`auth/config.go:23`](../core/internal/auth/config.go#L23)

```go
OIDCHttp bool `config:"flag=oicook,env=AUTH_OIDC_SECURE,default=true,usage=disable https only for OIDC"`
```

Quatre éléments, trois lectures contradictoires :

| Élément | Ce qu'il suggère |
|---|---|
| Nom du champ `OIDCHttp` | vrai = OIDC en HTTP → `Secure` faux |
| Variable `AUTH_OIDC_SECURE` | vrai = sécurisé → `Secure` vrai |
| Usage « disable https only » | vrai = désactive l'exigence HTTPS → `Secure` faux |
| Code `Secure: h.srv.config.OIDCHttp` | vrai → `Secure` **vrai** |

Le comportement est le bon — le défaut `true` donne `Secure: true`. C'est la
**documentation qui trompe** : quelqu'un qui lit l'usage et pose `true` pour désactiver
l'exigence HTTPS obtient exactement l'inverse, et son login OIDC échoue en HTTP avec
un symptôme incompréhensible (le cookie d'état n'est jamais renvoyé).

**Correction.** Renommer le champ en `OIDCCookieSecure` et réécrire l'usage :
« require HTTPS for the OIDC state cookie ». Aucun changement de comportement.

### 2.3 [MINEUR] Trois points

- `CheckAuth` renvoie `err.Error()` au client non authentifié
  ([`middleware.go:54`](../core/internal/auth/middleware.go#L54)) — divulgation
  d'information interne à qui n'est pas authentifié. Un message fixe suffit.
- `CreateAuthToken` ignore l'erreur de `rand.Int`
  ([`utils.go:54`](../core/internal/auth/utils.go#L54)). En pratique un échec du
  lecteur ferait paniquer sur `randomIndex.Int64()` — ce qui vaut mieux qu'un jeton
  faible, mais mérite d'être explicite plutôt que fortuit.
- `checkPassword` journalise en `Error` à **chaque** mot de passe faux. Un attaquant
  remplit les logs à volonté ; `Debug` est le bon niveau.

---

## 3. Système de fichiers — solide

La traversée de chemin est gardée **au bon endroit** : dans la couche
`filesystem`, pas dans les handlers. `ErrPathOutsideRoot` et le rejet explicite de
`..` sont implémentés dans les **deux** backends —
[local](../core/internal/host/filesystem/filesystem_local.go#L242) et
[sftp](../core/internal/host/filesystem/filesystem_sftp.go#L218). C'est exactement la
bonne architecture : un nouveau handler ne peut pas oublier la garde, elle est sous
lui.

Je n'ai pas trouvé de chemin d'accès contournant cette couche.

---

## 4. SSH — solide

Modèle **TOFU explicite** : à la première connexion la clé d'hôte est enregistrée, aux
suivantes elle est vérifiée par `ssh.FixedHostKey`
([`ssh.go:49`](../core/internal/ssh/ssh.go#L49)). C'est le modèle `known_hosts`, et
c'est le bon compromis pour un outil d'administration.

**Aucun `ssh.InsecureIgnoreHostKey` dans le dépôt.** C'est le raccourci le plus
fréquent de ce genre de projet ; il n'est pas pris ici.

---

## 5. WebSocket et serveur HTTP — solide

- `CheckOrigin` s'appuie sur une **liste blanche d'origines** globale
  ([`pkg/ws/ws.go`](../core/pkg/ws/ws.go)), avec une tolérance documentée pour les
  clients non-navigateurs sans en-tête `Origin`. Pas de `CheckOrigin: return true`,
  qui est l'erreur classique et qui ouvrirait le terminal hôte à n'importe quel site
  visité par l'utilisateur.
- Le serveur pose `ReadHeaderTimeout`, `IdleTimeout` et `MaxHeaderBytes`
  ([`server.go:55`](../core/internal/app/server.go#L55)) — les protections Slowloris
  sont présentes, ce qui est rarement le cas par défaut.

---

## 6. Couverture de tests — le point structurel

| Paquet | Lignes | Fichiers de test |
|---|---|---|
| `internal/lsp` | 517 | **0** |
| `internal/dockyaml` | 458 | **0** |
| `pkg/argos` | 400 | **0** |
| `internal/viewer` | 390 | **0** |
| `internal/desktop` | 247 | **0** |

`lsp` est identifié de longue date comme des stubs morts — le supprimer réduirait la
surface plutôt que d'ajouter des tests. Pour les autres, l'absence de test n'est
critique que là où le code mute quelque chose ; `dockyaml` et `viewer` méritent une
couverture, `argos` et `desktop` moins.

À l'inverse, `internal/docker` (29 fichiers de test), `gitsync` (19) et `secrets` (7)
sont désormais bien couverts — et `ContainerRecreateWithOptions`, qui n'avait
**aucun** test ce matin, en a sept, dont plusieurs qui échouent sur l'ancien
comportement.

---

## 7. État du backlog

**Corrigé aujourd'hui** : les 4 critiques, ~20 majeurs, la moitié des mineurs de la
revue du matin, plus deux constats trouvés en corrigeant — l'identité age non filtrée
par la synchronisation Git, et la dépendance du script de récupération à Dockman.

**Reste, par ordre d'importance :**

1. §2.1 et §2.2 ci-dessus — cookie `Secure` et documentation du drapeau OIDC.
2. `systemctl restart dockman-secrets-host` arrache les tmpfs des conteneurs en cours
   (`ExecStop=cleanup`). Non corrigé ; contournement documenté dans le cahier de test.
3. §2.2 de la revue du matin — la racine de stacks non mémorisée par hôte. Bloque
   aussi la persistance des champs du Rescue kit et du Host boot wizard. C'est le seul
   vrai item de conception restant, et il en débloquerait trois d'un coup.
4. Mineurs de la revue du matin : `SaveBinding` en `Unscoped()`, provenance de commit
   publiant le nom d'hôte sans opt-out, renommage d'hôte orphelinant les folder links.

---

## 8. Jugement d'ensemble

Le sous-système le plus exposé — l'authentification — est **correctement conçu** :
entropie du jeton, stockage haché, bcrypt, middleware fail-closed. Les deux majeurs
que je remonte sont un attribut de cookie commenté et une chaîne de documentation
inversée. Aucun défaut de conception.

Les endroits où ce type de projet se fait habituellement prendre —
`InsecureIgnoreHostKey`, `CheckOrigin` permissif, traversée de chemin dans les
handlers, jeton de session en clair en base — sont tous tenus.

Ce qui a changé aujourd'hui n'est pas seulement le nombre de bugs corrigés, mais le
fait que les propriétés qui comptent sont maintenant **vérifiées par la CI** plutôt
que par la vigilance : le rollback ne peut plus être court-circuité sans faire tomber
un test, ni le script de récupération redevenir dépendant de Dockman.
