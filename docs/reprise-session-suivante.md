# Reprise — points à traiter

État au 2026-08-07 en fin de session. Ce document est fait pour être lu **en premier**
par la session suivante.

---

## Où en est le dépôt

`integration` = `2de4c2d`, **Fork Checks et Fork Integration Build verts**.
Image `ghcr.io/cerede2000/dockman:integration` à jour.

Treize branches livrées et mergées dans la journée, chacune passée par sa CI avant
merge, `integration` revalidée après chaque merge. Elles restent poussées sur `origin`
(modèle de lots de PR upstream, voir [[dockman-pr-batching]]).

**Contraintes permanentes, non négociables** — les relire avant d'écrire du code :

- Répondre uniquement en **français**. Aucune mention d'IA dans les commits ou PR.
- **Zéro régression**, overhead minimal, sécurité maximale. Un correctif qui échange
  une propriété contre une autre est un arbitrage : il doit être **nommé**, pas enfoui.
- Les secrets ne doivent **jamais** dépendre de Dockman (voir
  [[dockman-secrets-independence]]).
- **Go 1.26.5 et Node sont disponibles localement** — compiler et tester localement
  d'abord, CI ensuite. Docker et le binaire compose restent absents : `TestVersion`,
  `TestUp`, `TestImageDive` échouent pour cette raison et sont **préexistants**
  (vérifier avec `git stash` avant de s'en attribuer la cause).
- Vérifier `git branch --show-current` avant tout commit ; une session parallèle
  travaille parfois dans le même dépôt.

---

## 1. §2.2 — la racine de stacks n'est pas mémorisée par hôte

**Le plus important : il débloque trois choses d'un coup.**

### Le défaut

`requestHostRuntimeReconcile` ([`inline.go`](../core/internal/secrets/inline.go)) écrit
le fichier de requête à la racine du `stackFS` de l'alias. L'unité systemd `.path`
surveille `config.StackRoot/.dockman-secrets-reconcile`.

Le cas qui casse : un alias **imbriqué** sous la racine configurée —
`StackRoot=/server/stacks`, alias `media` → `/server/stacks/media`. La stack est bien
découverte par le parcours de l'hôte, mais sa requête atterrit dans un fichier que
personne ne surveille. L'utilisateur voit un « en attente » qui ne se résout jamais.

En configuration mono-alias (le cas de l'utilisateur aujourd'hui), le comportement est
correct — d'où le report.

### Ce qu'il faut faire

La conception est arrêtée, **aucune décision à demander** :

1. Persister la racine de stacks sur l'enregistrement de l'hôte (table des hôtes,
   `internal/host`). Migration Gorm.
2. L'alimenter depuis **Réglages → Secrets → Host boot wizard**, qui la fait déjà
   saisir mais ne la garde pas.
3. L'exposer au résolveur pour que `requestHostRuntimeReconcile` écrive à la bonne
   place, avec repli sur le comportement actuel si elle n'a jamais été renseignée —
   **c'est ce repli qui garantit l'absence de régression**.

### Ce que ça débloque en plus

- La persistance des champs du **Host boot wizard** (aujourd'hui ressaisis à chaque
  fois).
- La persistance des champs du **Rescue kit**
  ([`tab-rescue.tsx`](../ui/src/pages/settings/tab-rescue.tsx)), même symptôme.

---

## 2. `SaveBinding` — lecture `Unscoped()`, écriture normale

[`gitsync/store.go:162`](../core/internal/gitsync/store.go#L162)

La lecture de contrôle utilise `s.db.Unscoped()`, donc elle voit les lignes en
suppression douce ; l'écriture qui suit est en portée normale. Soupçon : un lien
supprimé puis recréé avec le même UUID ferait passer le contrôle d'immutabilité
contre la ligne effacée, pendant que la mise à jour ne correspond à rien — succès
silencieux sans écriture.

**Non vérifié.** Commencer par écrire le test qui reproduit, avant de corriger. Si la
reproduction échoue, le constat tombe et il faut le dire.

---

## 3. Le renommage d'un hôte orpheline ses folder links

Le nom d'hôte est stocké comme clé dans les liens gitsync, sans propagation. Depuis la
garde d'immutabilité (`81b44ed`), la seule issue est de délier et relier — avec
reconstruction complète de la baseline.

**Une vraie décision produit à poser à l'utilisateur** avant de coder :

- propagation automatique du renommage vers les liens, ou
- flux de renommage explicite qui liste ce qui va être réécrit et demande confirmation.

La seconde est plus sûre mais plus lourde. Ne pas trancher seul.

---

## 3bis. Fait en toute fin de session — à connaître

**Suppression d'un dossier de stack chiffrée** (`feat/secrets-aware-folder-delete`).
Supprimer depuis Dockman le dossier d'une stack dont le tmpfs est monté partait à
moitié : `RemoveAll` supprimait `secrets.sops.yaml` puis échouait sur le point de
montage (EBUSY), laissant un dossier ni là ni parti et un montage orphelin. Une garde
libère maintenant le runtime volatil dans le cadre de la suppression, et refuse
purement si la libération n'aboutit pas. Elle réutilise `releaseVolatileRuntime`,
donc les deux sorties du mode chiffré se comportent à l'identique — les garder
alignées si l'une des deux évolue. Voir
[`delete_guard.go`](../core/internal/secrets/delete_guard.go).

---

## 4. En attente de l'utilisateur

**Le cahier de validation** —
[`cahier-validation-secrets-fr.md`](cahier-validation-secrets-fr.md). Sa **section 6**
est le seul test qui prouve l'indépendance : une VM neuve avec Docker et SOPS seuls,
la clé age restaurée, `./compose-sops.sh up` sur une stack chiffrée.

Si un point échoue, **il passe devant tout le reste** de ce document.

**Les PR upstream** — treize branches au chaud. Certaines intéresseraient RA341 (cycle
systemd, healthcheck réel, compensations sur contexte annulé) ; d'autres sont propres
au fork. Ne rien ouvrir sans que l'utilisateur le demande.

---

## 4bis. Surveiller les épingles exactes de `ui/package.json`

Le bloc `overrides` épingle des versions **exactes** (`"dompurify": "3.4.13"`,
`"glob": "13.0.2"`, …). Ces épingles existent pour de bonnes raisons — Electron et
electron-builder retiennent des majeurs legacy — mais elles ont deux effets de bord :

- elles figent la dépendance à l'endroit précis où le prochain avis de sécurité
  tombera ;
- elles neutralisent `npm audit fix`, qui ne touche pas à un override explicite.

C'est arrivé le 2026-08-07 : l'avis `GHSA-55q2-fjhq-7xh7` a rendu
`dompurify <= 3.4.12` vulnérable, or l'override épinglait exactement `3.4.12`.
`Fork Checks` s'est mis à échouer sur toutes les branches sans qu'une ligne de code
ait bougé, et `npm audit fix` s'est contenté de monter `js-yaml` sans rapport. Il a
fallu remonter l'épingle à la main.

À faire : revoir périodiquement ces épingles, et passer en `^` celles qui le
permettent. Un `Fork Checks` rouge sans changement de code, c'est ce réflexe-là
qu'il faut avoir.

---

## 5. À ne pas faire

**Ne pas supprimer `internal/lsp`** (517 lignes, 0 test, importé nulle part). C'est du
code **upstream** : le retirer sur `integration` créerait un conflit à chaque merge
upstream futur, pour zéro gain à l'exécution. C'est une PR à proposer à RA341. J'avais
suggéré l'inverse dans une revue — c'était une erreur.

**Ne pas se fier aux tests comme preuve d'intention.** Trois fois dans la journée un
test affirmait un défaut, ce qui le faisait passer pour un choix délibéré et le rendait
invisible à la revue :

- `require.Contains(script, "dockman-secrets-reconcile.service")` verrouillait la
  dépendance du script de récupération à Dockman.
- `require.Contains(unit, "ExecStop=")` verrouillait le démontage des secrets à chaque
  réinstallation.
- `require.Contains(commit.Message, 'host="local"')` verrouillait la divulgation du nom
  d'hôte dans les commits publics.

Quand un test paraît défendre un comportement douteux, se demander s'il ne l'entérine
pas simplement.

---

## 6. Méthode qui a fonctionné, à reconduire

**Prouver qu'un test échoue sur l'ancien comportement.** Copier le fichier dans `/tmp`,
neutraliser le correctif au point exact (`if true { return nil }`, `if false && ...`),
lancer les tests ciblés, noter les noms qui tombent, restaurer par `cp`, relancer la
suite complète. Le rapporter explicitement à l'utilisateur.

C'est ce qui a distingué, tout au long de la journée, les tests qui démontrent de ceux
qui décorent. Voir [[test-prove-old-behavior-fails]].

**Deux régressions ont été livrées puis rattrapées** après un rappel de l'utilisateur
sur les contraintes : la suppression d'`ExecStop` (qui ne sortait plus le clair de la
mémoire à l'arrêt) et le passage de l'échec de mot de passe en `Debug` (qui masquait
les tentatives de force brute). Dans les deux cas, le symptôme signalé avait été
corrigé en déplaçant le coût ailleurs sans le nommer. Le vrai coupable était en amont :
l'activation par `systemctl restart`, pas le nettoyage lui-même.
