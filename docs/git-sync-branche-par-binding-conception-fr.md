# Branche par folder link — document de conception

**Date** : 29 juillet 2026
**Base de référence** : `integration` @ `e88523c`
**Nature** : spécification. Aucun code n'est fourni ; les emplacements cités servent à mesurer l'impact.

---

## 1. Faut-il vraiment ce mécanisme ?

Question légitime : Dockman gère déjà plusieurs hôtes, chacun avec son propre folder link. La séparation d'environnements semble donc déjà couverte.

**C'est vrai pour la séparation du contenu.** Deux hôtes peuvent pointer vers deux sous-dossiers différents du même dépôt :

```
dépôt homelab
├── stacks/nextcloud/        ← folder link de l'hôte prod
└── stacks-test/nextcloud/   ← folder link de l'hôte test
```

Deux contenus indépendants, une seule branche, aucun besoin de ce chantier. **Pour le cas le plus courant — la même stack déployée sur plusieurs hôtes — un dépôt, une branche et N liens suffisent, et ce mécanisme n'apporterait rien.**

**Ce que l'approche par dossiers ne donne pas, c'est la promotion.** Avec deux dossiers, faire passer une version validée du test vers la production est une **copie de fichiers**. Git l'enregistre comme une modification quelconque, sans lien avec ce qui a été testé. On ne peut ni dire « la prod est trois commits derrière le test », ni annuler la promotion d'un geste, ni garantir que ce qui part en production est exactement ce qui a été validé — seulement que quelqu'un a copié ce qu'il croyait être la bonne version.

Avec deux branches, la promotion est une **fusion** : le contenu promu est bit pour bit celui qui a été testé, l'écart entre les deux environnements est mesurable à tout moment, et le retour arrière est un `revert` de la fusion.

**Conclusion** : ce mécanisme n'est justifié que si un cycle de validation avant production est recherché. Il ne l'est pas pour dupliquer une stack sur plusieurs machines. Le chantier est à engager **uniquement** si la phase 2 (§8) est visée — sans elle, l'apport reste marginal par rapport aux dossiers.

---

## 2. Objectif

Permettre à chaque folder link de suivre **sa propre branche** du dépôt auquel il est rattaché, au lieu de partager la branche unique du dépôt.

État actuel : `Repository.DefaultBranch` (`models.go:34`) est la seule branche connue, utilisée par tous les liens du dépôt.

Contournement existant : déclarer deux fois la même URL distante sous deux noms et deux branches. C'est possible — l'unicité porte sur le couple identité + branche (`repository.go:210`) — mais cela duplique le clone, les credentials et l'historique d'opérations, et les deux dépôts sont invisibles l'un à l'autre.

---

## 3. Modèle de données

Ajouter à `StackBinding` (`models.go:45`) :

```
Branch string `gorm:"not null;default:''"`
```

Valeur vide = repli sur `Repository.DefaultBranch`. Aucune reprise de données n'est nécessaire : les liens existants gardent le comportement actuel.

Une fonction unique de résolution doit être introduite et devenir **le seul point** qui décide de la branche d'un lien. Toute lecture directe de `DefaultBranch` sur un chemin lié à un binding devient un défaut.

Les baselines sont déjà indexées par binding (`BindingBaseline.BindingUUID`, `models.go:82`) : aucune migration de ce côté.

---

## 4. Contrainte architecturale principale

Le clone est créé en **mono-branche** (`repository.go:888`) :

```
ReferenceName: plumbing.NewBranchReferenceName(row.DefaultBranch), SingleBranch: true
```

C'est le point structurant du chantier : tant que le clone ne suit qu'une référence, aucune autre branche n'est accessible localement.

Deux options :

- **A — suivre les branches utilisées.** Le refspec de fetch est construit à partir des branches réellement référencées par les liens du dépôt, plus sa branche par défaut. Le volume reste maîtrisé, mais le refspec devient dynamique et doit être recalculé quand un lien change de branche.
- **B — suivre toutes les têtes.** Refspec `+refs/heads/*:refs/remotes/origin/*`. Plus simple et robuste, au prix du téléchargement des branches inutilisées. Sur un dépôt de fichiers Compose, le surcoût est négligeable ; sur un dépôt partagé avec du code applicatif, il ne l'est pas.

**Recommandation : option A**, avec repli sur B si le recalcul du refspec s'avère fragile. Dans les deux cas, le stockage compact (objets seuls, sans copie de travail) accueille plusieurs branches sans difficulté.

---

## 5. Chemins de code impactés

`DefaultBranch` apparaît à une trentaine d'endroits. Seuls ceux liés à un binding doivent changer ; ceux qui décrivent le dépôt lui-même restent inchangés.

### Lecture — modification triviale

`collectRepositoryFiles` prend **déjà** la branche en paramètre (`binding.go:1866`). Il suffit de lui passer la branche du lien au lieu de celle du dépôt :

- `binding.go:1751` — chargement des arbres de transfert.
- `binding.go:714` — `repositoryComposeCatalog`. **Point critique** : cette fonction alimente le catalogue Compose, devenu bloquant depuis le passage en fail-closed du profil `compose_only`. Un catalogue lu sur la mauvaise branche produirait une erreur incompréhensible pour l'utilisateur.

### Écriture — modification réelle

Trois emplacements créent un commit via un checkout temporaire vers `DefaultBranch` :

- `binding.go:1079` — export d'un lien.
- `local_deletion.go:264` et `:410` — suppressions propagées vers Git.

Chacun doit viser la branche du lien. Si elle n'existe pas encore localement, la créer depuis la référence distante correspondante, ou depuis la branche par défaut du dépôt lorsque la branche est nouvelle.

Le push est déjà paramétré par branche (`repository.go:381` et `:425`, refspec `refs/heads/X:refs/heads/X`) : seule la valeur transmise change.

### Historique et rollback

- `commit_rollback.go:93` — liste des commits d'un lien.
- `commit_rollback.go:333` — `reachableBindingCommit`.

Les empreintes de commit sont indépendantes des branches, mais l'accessibilité ne l'est pas : un commit joignable depuis `main` ne l'est pas nécessairement depuis `test`. Ces deux appels doivent utiliser la branche du lien, sans quoi un rollback pourrait proposer des commits appartenant à un autre environnement.

### Statut

- `repository.go:557` — `RepositoryGitStatus` calcule avance et retard sur `DefaultBranch` uniquement.
- `repository.go:635` et `:724` — réinitialisation sur la référence distante.
- `stack_status.go:167` et `:198` — `RepositoryBranch` exposé aux badges de l'interface.

Voir §6.

---

## 6. Statut et divergence par branche

Aujourd'hui le statut est **par dépôt**. Avec des branches par lien, un dépôt peut être à jour sur une branche et divergent sur une autre : la notion perd son sens au niveau du dépôt.

Proposition :

- `RepositoryGitStatus` reste calculé sur la branche par défaut et continue de décrire la santé du clone.
- Un **statut par lien** est ajouté, portant la branche du lien, son avance, son retard et sa divergence. C'est lui qu'affichent la ligne du folder link et les badges de stack.
- L'action « Reset to remote » (`repository.go:701`) devient paramétrée par branche. Sa confirmation textuelle doit nommer la branche visée, faute de quoi l'opérateur ne saura pas ce qu'il réinitialise.

---

## 7. Comportement critique — changement de branche d'un lien existant

**C'est le point le plus dangereux du chantier.**

La baseline associe un chemin à l'empreinte du dernier transfert réussi. Changer la branche d'un lien change le contenu de référence **sans changer un seul fichier local**. Une baseline conservée telle quelle décrirait alors un état qui n'a jamais existé sur la nouvelle branche : Dockman croirait certains fichiers synchronisés alors qu'ils diffèrent, et un import pourrait écraser des modifications locales en les prenant pour identiques.

Comportement exigé lors d'un changement de branche :

1. **Purger la baseline du lien.** Tous les écarts repassent en conflit initial `no_baseline`, ce qui force une prévisualisation explicite avant toute écriture. C'est bruyant, et c'est voulu.
2. **Mettre en pause la synchronisation automatique** du lien, comme le fait déjà le rollback par commit.
3. **Exiger une confirmation textuelle** rappelant que la baseline sera perdue et que les écarts devront être re-arbitrés.
4. **Rafraîchir le catalogue Compose** sur la nouvelle branche, puisqu'elle peut contenir un ensemble de stacks différent.
5. **Journaliser l'opération** dans l'activité du lien, avec l'ancienne et la nouvelle branche.

Aucune écriture, ni locale ni distante, ne doit accompagner le changement de branche lui-même.

---

## 8. Phase 2 — la promotion

Sans elle, ce chantier n'apporte que la capacité de pointer ailleurs. C'est la promotion qui justifie l'investissement.

**Action proposée** : depuis un folder link, promouvoir sa branche vers une autre branche du même dépôt.

Déroulement :

1. Choix de la branche cible parmi celles du dépôt.
2. Prévisualisation de l'écart : commits à promouvoir, fichiers touchés, comparaison Monaco réutilisant le composant existant.
3. **Fusion en avance rapide uniquement.** Si la cible a divergé, refuser et renvoyer vers la résolution de conflits. Dockman n'a pas de résolveur de fusion, et il ne doit pas en improviser un.
4. Push de la branche cible.
5. Les liens abonnés à la branche cible détectent le nouveau commit à leur cycle suivant et se synchronisent selon leur propre politique — **aucun déploiement implicite** n'est déclenché par la promotion.

Le retour arrière est la responsabilité de Git : un `revert` de la fusion sur la branche cible, exposé comme une opération normale.

---

## 9. Interface

**Dialog du folder link** : champ « Branche », pré-rempli avec la branche du dépôt, accompagné d'un sélecteur listant les branches distantes connues. L'option « créer depuis… » réutilise le parcours existant de branche manquante (`tab-git.tsx:507`, flux `remote_branch_missing`).

**Tableau des folder links** : la branche apparaît à côté du dépôt, avec un marquage visuel quand elle diffère de la branche par défaut — le cas particulier doit se voir.

**Changement de branche** : dialog dédié portant l'avertissement de perte de baseline et la confirmation textuelle du §7.

**Badges de stack** : `RepositoryBranch` est déjà exposé (`stack_status.go:167`) et affiché dans le popover ; il doit refléter la branche du lien et non celle du dépôt.

**Promotion** : entrée dans le menu d'actions du folder link, ouvrant le dialog de prévisualisation décrit au §8.

---

## 10. Non-objectifs

- **Plusieurs dépôts pour un même lien.** Deux sources pour une unité déployable créent deux vérités sans transaction commune : rien ne garantit que les deux avancent ensemble, et le retour arrière devient indéterminé. La séparation par dossiers dans un dépôt unique couvre le besoin en conservant un commit unique décrivant l'ensemble.
- **Résolution de fusion dans l'application.** La promotion est en avance rapide ou refusée.
- **Déploiement automatique déclenché par une promotion.** Les liens abonnés décident selon leur propre politique.
- **Réécriture d'historique.**

---

## 11. Risques et cas limites

| Cas | Traitement attendu |
|---|---|
| Branche absente en local et à distance | Création depuis la branche par défaut du dépôt, ou branche vide orpheline — parcours existant réutilisé |
| Deux liens sur la même branche et le même sous-chemin | Situation déjà possible aujourd'hui ; le verrou de dépôt sérialise, le conflit de baseline protège |
| Suppression distante d'une branche utilisée | Le lien passe en erreur explicite, jamais en synchronisation silencieuse sur une autre branche |
| Concurrence entre deux liens sur deux branches | Conserver le verrou **au niveau du dépôt** : les branches partagent le même magasin d'objets |
| Sauvegardes créées avant le changement de branche | Restent restaurables ; leur manifeste doit porter la branche d'origine |
| Rollback vers un commit d'une autre branche | Interdit — `reachableBindingCommit` filtre par accessibilité depuis la branche du lien |

---

## 12. Plan de livraison

**Lot 1 — socle de lecture.** Champ `Branch`, résolution centralisée, refspec multi-branches, propagation vers les chemins de lecture (`binding.go:1751`, `binding.go:714`). Aucune écriture n'est encore concernée. Un lien peut lire une autre branche ; l'export reste bloqué sur la branche par défaut.

**Lot 2 — écriture.** Export et suppressions ciblent la branche du lien. Push paramétré. Historique et rollback filtrés par accessibilité.

**Lot 3 — statut et interface.** Statut par lien, réinitialisation par branche, champ et sélecteur dans le dialog, changement de branche avec purge de baseline et confirmation.

**Lot 4 — promotion.** Prévisualisation, fusion en avance rapide, push.

Les lots 1 à 3 sont livrables indépendamment et laissent le produit cohérent à chaque étape. Le lot 4 est celui qui porte la valeur ; **engager les trois premiers sans lui reviendrait à payer la complexité sans en tirer le bénéfice**.

---

## 13. Tests attendus

1. Deux liens du même dépôt sur deux branches différentes se synchronisent sans interférence.
2. Un lien sans branche explicite conserve exactement le comportement actuel — non-régression.
3. Le changement de branche purge la baseline, met l'automatisation en pause et n'écrit aucun fichier.
4. Le catalogue Compose est relu sur la nouvelle branche ; en profil `compose_only`, un catalogue vide sur la nouvelle branche échoue fermé avec le message existant.
5. Le rollback ne propose que des commits joignables depuis la branche du lien.
6. La promotion en avance rapide impossible est refusée avec un message explicite, sans modification d'aucune branche.
7. La suppression distante d'une branche utilisée met le lien en erreur et ne bascule sur aucune autre branche.
