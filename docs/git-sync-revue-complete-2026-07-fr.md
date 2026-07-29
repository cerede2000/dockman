# Revue complète du fonctionnement Git Sync

**Projet :** Dockman Git Edition  
**Date de la revue :** 29 juillet 2026  
**Branche auditée :** `integration`  
**Périmètre :** synchronisation Git, stockage, sécurité, automatisation, déploiement, rollback, provisioning, sauvegardes et interfaces Files/Monitor/Settings.

## 1. Résumé exécutif

Le moteur Git Sync est fonctionnel et couvre désormais le cycle de vie complet attendu pour gérer des stacks Compose depuis Dockman et Git :

- création ou import d'un dépôt GitHub public ou privé ;
- liaison d'un dossier complet de stacks avec un dossier ou la racine d'un dépôt ;
- sélection précise des stacks suivies ;
- synchronisation manuelle dans les deux sens ;
- surveillance automatique Git vers Dockman ;
- comparaison et résolution individuelle des conflits ;
- traitement explicite et protégé des suppressions ;
- sauvegardes, restauration et rollback vers un commit ;
- validation, déploiement contrôlé et rollback automatique ;
- provisioning déclaratif borné via `provision.yml` ou `provision.yaml` ;
- statut compact dans Settings, Files et Monitor ;
- historique des opérations et rétention automatique.

L'architecture respecte la ligne directrice définie pendant les développements : comportement non destructif par défaut, actions sensibles explicites, isolation des erreurs par stack, faible activité CPU au repos, consommation mémoire bornée et absence de copie permanente complète des stacks.

La revue a identifié et corrigé un cas limite supplémentaire : lorsqu'une stack racine et des sous-stacks sont toutes configurées pour le déploiement automatique, un fichier d'une sous-stack pouvait aussi sélectionner la stack racine. La sélection utilise désormais systématiquement la stack propriétaire la plus profonde.

### Conclusion de la revue

| Domaine | État | Conclusion |
|---|---:|---|
| Synchronisation manuelle | ✅ Implémenté | Bidirectionnelle, prévisualisée et protégée |
| Synchronisation automatique | ✅ Implémenté | Git → Dockman, opt-in, bornée et interruptible |
| Dockman → Git automatique | ⛔ Non implémenté | Le push reste volontairement manuel |
| Conflits | ✅ Implémenté | Détection 3-voies et résolution fichier par fichier |
| Suppressions | ✅ Implémenté | Préservées par défaut, résolution explicite |
| Déploiement automatique | ✅ Implémenté | Opt-in par stack, validation et dry-run préalables |
| Rollback automatique | ✅ Implémenté | Restauration des fichiers et redéploiement protégés |
| Provisioning | ✅ Implémenté | Déclaratif, sans shell, confiné et réversible |
| Sauvegardes | ✅ Implémenté | Vérifiées, restaurables et soumises à rétention |
| Sécurité des identifiants | ✅ Implémenté | Chiffrement AES-GCM et clé maître externe possible |
| Multi-dépôts / multi-stacks | ✅ Implémenté | Plusieurs dépôts et plusieurs folder links |
| Fournisseurs Git | ⚠️ Partiel | GitHub uniquement pour le moment |
| Webhooks | ⛔ Non implémenté | Polling maîtrisé uniquement |
| Signature cryptographique des commits | ⛔ Non implémenté | Auteur et provenance configurables, sans signature |

Il n'est pas possible de garantir qu'un logiciel ne contiendra jamais aucun bug. En revanche, la combinaison revue statique, tests automatisés, garde-fous et scénarios d'acceptation détaillés ci-dessous donne un niveau de confiance élevé pour la mise en test d'intégration.

## 2. Architecture et flux de données

### 2.1 Composants

Le backend Git Sync est isolé dans `core/internal/gitsync` :

- **Service Git :** dépôts, branches, fetch, push et état ahead/behind/diverged ;
- **Vault :** chiffrement et déchiffrement des credentials ;
- **Bindings :** association hôte + dossier Dockman + destination Git ;
- **Policy engine :** profils, inclusions, exclusions et fichiers sensibles ;
- **Baseline 3-voies :** dernière version synchronisée servant à différencier changement local, Git et conflit ;
- **Automation :** ordonnanceur à faible fréquence et verrouillage par binding/dépôt ;
- **Deployment :** validation, dry-run, déploiement, attente de santé et rollback ;
- **Provisioning :** opérations de filesystem déclaratives et transactionnelles ;
- **Backup/recovery :** archives, manifestes, restauration et rollback de commit ;
- **Status projection :** index compact consommé par Files et Monitor ;
- **HTTP API :** routes protégées sous `/api/protected/git`.

Les données persistantes sont séparées :

- **SQLite :** configuration, baselines, états, opérations, déploiements et métadonnées de sauvegarde ;
- **stockage Git :** objets Git compacts sans worktree permanent ;
- **stockage backup :** archives `tar.gz` et manifestes.

### 2.2 Flux Git vers Dockman

1. Verrouillage du binding et du dépôt.
2. Fetch borné dans le temps.
3. Construction de l'inventaire selon le profil et les règles.
4. Comparaison Git / local / baseline.
5. Arrêt sans mutation en cas de conflit ou d'éditeur local sale sur la stack concernée.
6. Sauvegarde des éléments concernés.
7. Transfert borné des fichiers autorisés.
8. Provisioning déclaratif éventuel.
9. Validation Compose, puis dry-run si le déploiement est activé.
10. Déploiement des stacks sélectionnées, indépendamment les unes des autres.
11. Contrôle de l'état ; rollback si activé et nécessaire.
12. Mise à jour des baselines, statuts et historique.

### 2.3 Flux Dockman vers Git

1. Prévisualisation des différences selon la politique active.
2. Sélection manuelle des fichiers.
3. Vérification du jeton de preview après prise du verrou.
4. Refus si le contenu a changé depuis la preview.
5. Création d'un worktree temporaire.
6. Copie des seuls éléments sélectionnés.
7. Commit avec auteur configuré et provenance Dockman.
8. Push puis suppression du worktree temporaire.
9. Mise à jour des baselines, statuts et historique.

Ce sens reste manuel : aucun changement local n'est envoyé silencieusement vers Git.

## 3. Règles fonctionnelles consolidées

### 3.1 Principes généraux

| Règle | État | Vérification |
|---|---:|---|
| Git Sync est désactivé par défaut | ✅ | `DOCKMAN_GIT_SYNC=false` par défaut |
| Un transfert normal ne supprime jamais silencieusement la source opposée | ✅ | suppressions transformées en états à résoudre |
| Une importation Git ne déploie pas sans option explicite | ✅ | auto-deploy séparé et opt-in |
| Une erreur dans une stack ne bloque pas les stacks indépendantes | ✅ | traitement et résultats par stack |
| Un fichier hors politique ne doit pas être ouvert | ✅ | parcours confiné par profil, y compris dossiers secrets/data |
| Une preview devient invalide si les fichiers changent avant validation | ✅ | jeton recalculé sous verrou |
| Les conflits peuvent rester partiellement non résolus | ✅ | décisions fichier par fichier |
| Les éditeurs sales ne sont jamais écrasés | ✅ | cohérence éditeur et blocage ciblé |
| Un éditeur propre reçoit les changements externes | ✅ | notification/rechargement externe |
| Les opérations longues sont bornées | ✅ | limites d'inventaire, tailles et timeout réseau |
| Les actions sont historisées | ✅ | opérations dépôt/binding/stack |

### 3.2 Priorité des règles de fichiers

L'évaluation suit cette logique :

1. confinement dans la racine du folder link ;
2. rejet des symlinks ou chemins spéciaux dangereux ;
3. identification des fichiers de contrôle protégés ;
4. profil sélectionné ;
5. exclusions globales du dépôt ;
6. exclusions du folder link et `.dockmanignore` ;
7. inclusions explicites ;
8. contrôle des fichiers sensibles ;
9. limites de taille et d'inventaire.

Une inclusion explicite peut réautoriser un fichier fonctionnel exclu par le profil, mais elle ne contourne pas les protections structurelles, les limites absolues ni la confirmation requise pour un secret.

### 3.3 Profils de synchronisation

#### Docker Compose only

**Objectif :** synchroniser uniquement les définitions de stacks et leurs modèles d'environnement.

Inclus automatiquement :

- `compose.yml`, `compose.yaml` ;
- `docker-compose.yml`, `docker-compose.yaml` ;
- `.env.example`, `.env.sample`, `.env.template`, `.env.dist` ;
- variantes telles que `.env.production.example` ou `.env.foo.template`.

Ne sont pas inclus automatiquement :

- les autres fichiers `.yml` ou `.yaml` ;
- les dossiers `data`, `secrets`, caches ou contenus applicatifs ;
- les vrais fichiers `.env`.

Des fichiers supplémentaires, par exemple `config/app.conf`, peuvent être ajoutés explicitement. Le moteur ne parcourt alors que les chemins pertinents ; il ne tente pas de lire les sous-dossiers sans rapport.

**État :** ✅ implémenté et corrigé après les régressions `.env.example` et lecture des sous-dossiers secrets.

#### Compose + configuration

**Objectif :** synchroniser les stacks et les fichiers de configuration usuels.

Extensions reconnues : YAML, JSON, TOML, CONF, CONFIG, CFG, INI, properties, XML, templates, scripts shell, SQL, texte, Markdown et certificats publics. Des noms usuels tels que `Dockerfile`, `Containerfile`, `Caddyfile`, `Makefile`, `.gitignore`, `.dockerignore` et `.dockmanignore` sont également reconnus.

Les secrets restent filtrés indépendamment du profil.

**État :** ✅ implémenté.

#### All files

**Objectif :** synchroniser tout le contenu admissible du dossier lié.

Les exclusions, secrets, chemins spéciaux et limites restent actifs. « All files » ne signifie donc jamais « contourner la sécurité ».

**État :** ✅ implémenté.

### 3.4 Fichiers sensibles

Sont notamment considérés sensibles :

- vrais fichiers `.env` et variantes non marquées example/sample/template/dist ;
- clés privées SSH ;
- fichiers `.pem`, `.key`, `.p12`, `.pfx` ;
- noms contenant des termes tels que secret ou credential.

Règles :

- exclus par défaut ;
- inclusion manuelle possible uniquement avec confirmation explicite ;
- jamais inclus automatiquement par la surveillance ;
- les backups peuvent contenir les secrets autorisés manuellement et doivent donc être protégés au niveau du volume.

**État :** ✅ implémenté.  
**Limite :** ⚠️ les archives de backup ne sont pas chiffrées par Dockman.

## 4. Dépôts, branches et credentials

### 4.1 Formats d'URL acceptés

- `owner/repository` ;
- `https://github.com/owner/repository` avec ou sans `.git` ;
- formes SSH GitHub validées.

Les URL contenant des credentials, des query strings suspectes, des chemins imbriqués invalides ou des ports SSH non permis sont rejetées.

### 4.2 Dépôts et branches

- dépôt public sans credential ;
- dépôt privé par token HTTPS ;
- dépôt privé par clé SSH ;
- création d'un dépôt GitHub ;
- import d'un dépôt existant ;
- validation de la branche à l'ajout ;
- création confirmée d'une branche manquante depuis la branche par défaut ou comme branche indépendante vide ;
- refus d'enregistrer deux fois la même paire dépôt + branche ;
- état local calculé : up-to-date, ahead, behind ou diverged ;
- reset explicite de la référence Git locale vers le remote avec confirmation typée.

Le reset du dépôt ne touche ni les stacks locales ni leurs baselines.

**État :** ✅ implémenté pour GitHub.  
**Limite :** ⚠️ la branche est définie au niveau du dépôt enregistré, pas individuellement par folder link.

### 4.3 Credentials

- secrets chiffrés en base avec AES-GCM ;
- AAD liée à l'identifiant du credential ;
- secret jamais renvoyé par l'API ;
- clé maître de 32 octets ou base64 via `DOCKMAN_GIT_MASTER_KEY_FILE` ;
- génération locale en mode permissif si aucun secret n'est fourni, avec avertissement ;
- cache borné des clés hôtes SSH ;
- test de credential avant utilisation.

**État :** ✅ implémenté.  
**Recommandation production :** monter la clé maître comme Docker secret, sauvegarder cette clé séparément et protéger le volume SQLite.

### 4.4 Identité des commits

- auteur et email configurables par dépôt ;
- valeurs par défaut non ambiguës ;
- trailer `Dockman-Origin` indiquant instance, hôte, binding et stack ;
- caractères de contrôle supprimés et longueur bornée.

**État :** ✅ identité et provenance implémentées.  
**Non implémenté :** ⛔ signature GPG/SSH cryptographique des commits.

## 5. Folder links et sélection des stacks

Un folder link associe :

- un hôte Dockman ;
- un dossier complet sous la racine des stacks ;
- un dépôt enregistré ;
- un sous-dossier Git, éventuellement la racine ;
- un profil et ses règles ;
- toutes les stacks découvertes ou une sélection explicite.

### Règles

- plusieurs dépôts et plusieurs links sont permis ;
- les recouvrements locaux ou Git ambigus sont refusés ;
- la hiérarchie complète des sous-dossiers est conservée ;
- la découverte des Compose est en largeur, avec profondeur et volume bornés ;
- la sélection supporte recherche, filtres, tout/rien et mémorise les choix entre filtres ;
- l'ajout ou la suppression locale d'une stack rafraîchit le catalogue ;
- une nouvelle stack sous un dossier lié apparaît non sélectionnée et peut être activée puis poussée directement ;
- le nombre de fichiers Compose est affiché et la liste détaillée est modifiable ;
- relier à nouveau conserve la baseline, sauf demande explicite d'oubli.

### Initialisation

Modes disponibles :

- **aucun transfert** : crée seulement le lien ;
- **Dockman → Git** : pousse l'état local initial ;
- **Git → Dockman** : importe l'état Git initial ;
- **auto-réconciliation** : si les arbres admissibles sont identiques, l'état devient immédiatement synchronisé sans transfert.

**État :** ✅ implémenté.

## 6. États et conflits

### 6.1 Baseline 3-voies

Pour chaque fichier admissible, le moteur compare :

- le contenu local actuel ;
- le contenu Git actuel ;
- la dernière baseline acceptée.

Cela permet de distinguer :

- identique ;
- nouveau local ;
- nouveau Git ;
- modifié localement ;
- modifié sur Git ;
- supprimé localement ;
- supprimé sur Git ;
- conflit réel, lorsque les deux côtés ont divergé depuis la baseline.

Un conflit peut donc apparaître principalement dans le sens Git → Dockman, car c'est ce transfert qui risquerait d'écraser la modification locale. La projection globale reste néanmoins en conflit.

### 6.2 Résolution

- comparaison du fichier ou des blocs texte ;
- choix Git ou Dockman séparément pour chaque conflit ;
- résolution d'un seul conflit sans forcer les autres ;
- sélection multiple et pagination pour les grands inventaires ;
- nouveau contrôle automatique après résolution ;
- reprise de la surveillance lorsque l'état est redevenu cohérent.

Les comparaisons de contenu sont bornées à 2 MiB. Au-delà, les métadonnées restent disponibles mais pas le diff texte complet.

**État :** ✅ implémenté et corrigé pour les vues vides causées par un filtre de recherche restant actif.

## 7. Suppressions et orphelins

### 7.1 Suppression sur Git, fichier encore local

Le comportement par défaut est **preserve local**. L'utilisateur choisit ensuite :

- restaurer le fichier/la stack vers Git ;
- archiver localement ;
- sauvegarder puis supprimer explicitement le local.

Une suppression locale de stack n'exécute pas implicitement `compose down` et ne supprime jamais les volumes Docker.

### 7.2 Suppression locale, fichier encore sur Git

Choix disponibles :

- restaurer depuis Git ;
- supprimer explicitement sur Git avec confirmation ;
- retirer/exclure l'élément de la synchronisation.

Après une résolution valide, l'état et la baseline sont réconciliés immédiatement : il n'est pas nécessaire de réenregistrer la configuration du folder link.

### 7.3 Limite volontaire

Il n'existe pas encore de politique automatique configurable `preserve / archive / mirror-delete`. La conservation sûre et la décision manuelle sont imposées.

**État :** ✅ traitement manuel complet ; ⚠️ politiques automatiques non implémentées.

## 8. Automatisation

### 8.1 Surveillance Git vers Dockman

- désactivée par défaut et activée par folder link ;
- intervalle de 5 minutes à 24 heures ;
- échéance calculée depuis le dernier passage, sans dérive d'une minute ;
- un seul ordonnanceur léger ;
- sommeil maximal de 30 secondes pour prendre en compte les changements de configuration ;
- verrou par binding et par dépôt ;
- timeout réseau de 3 minutes ;
- un échec n'empêche pas le traitement des autres bindings ;
- pause globale par folder link et pause individuelle par stack ;
- reprise = contrôle complet immédiat ;
- un commit Git identique utilise un chemin rapide ;
- si l'état d'une stack est rouge/partiel, le même commit déclenche néanmoins une nouvelle vérification de santé ;
- aucun push local automatique.

**État :** ✅ implémenté.

### 8.2 Découverte automatique de nouvelles stacks

- opt-in séparé ;
- une nouvelle définition Compose est validée, dry-run puis déployée ;
- maximum 10 nouvelles stacks par cycle ;
- chaque stack est traitée indépendamment ;
- une stack invalide ne bloque pas la création ou la mise à jour des autres.

**État :** ✅ implémenté.

### 8.3 Consommation au repos

Le statut affiché n'effectue pas de scan filesystem continu. Il provient d'une projection SQLite compacte, rafraîchie par événements et lors des cycles configurés. Les dossiers parents agrègent les états enfants sans polling supplémentaire.

Les mesures utilisateur observées au repos étaient généralement :

- Dockman : 0 % la plupart des échantillons, avec pics courts lors d'un scan/sync ;
- mémoire : environ 17 à 24 MiB dans le scénario observé ;
- socketproxy : activité principalement lors des inventaires Docker et déploiements.

**État :** ✅ conforme à l'objectif de faible overhead.  
**Point de surveillance :** de nombreux dépôts injoignables peuvent retarder les suivants, car l'ordonnanceur est séquentiel même si chaque accès est borné à 3 minutes.

## 9. Déploiement contrôlé et rollback

### 9.1 Déploiement

Le déploiement automatique est une option distincte, activée uniquement pour des stacks choisies.

Séquence :

1. transfert Git réussi ;
2. provisioning éventuel ;
3. validation Compose via le service Dockman existant ;
4. dry-run Compose ;
5. `up` avec la mécanique Dockman existante ;
6. prise en compte de la cascade `.env` déjà supportée par Dockman ;
7. attente de l'état attendu si rollback activé.

Les logs de Compose sont nettoyés des séquences de terminal et bornés à 256 KiB.

### 9.2 Attribution d'un fichier à une stack

La règle est désormais uniforme : **la stack dont le répertoire est le plus profond possède le fichier**.

Exemple : avec `compose.yml` et `apps/demo/compose.yml`, une modification de `apps/demo/config.yml` ne redéploie que `apps/demo/compose.yml`. Un fichier racine reste attribué à la stack racine.

**État :** ✅ corrigé pendant cette revue.

### 9.3 Rollback automatique

En cas d'échec de validation, provisioning, déploiement ou contrôle de santé :

- restauration des seuls fichiers concernés depuis la sauvegarde pré-import ;
- restauration des effets de provisioning suivis ;
- protection contre l'écrasement d'une modification concurrente ;
- nettoyage de l'essai défaillant ;
- redéploiement de la version précédente ;
- état explicite `rolled back` ou `rollback failed` ;
- reprise possible par push/resume sans double action inutile.

Le rollback détecte une boucle de restart dans la fenêtre d'attente. Une panne très différée ne peut être détectée de manière fiable que si la stack possède un healthcheck significatif.

**État :** ✅ implémenté.  
**Recommandation :** définir un healthcheck réel sur les services critiques.

## 10. Provisioning déclaratif

Le provisioning remplace volontairement l'exécution de scripts arbitraires.

### Format et opérations

- fichier Git-only `provision.yml` ou `provision.yaml` ;
- schéma versionné et parsing YAML strict ;
- taille maximale 64 KiB ;
- maximum 128 opérations ;
- création de dossiers ;
- `chmod` ;
- `chown` ;
- suppression protégée de fichiers ou dossiers.

### Sécurité

- aucune commande shell ;
- chemins relatifs confinés dans la stack ;
- symlinks et fichiers spéciaux rejetés ;
- impossibilité de supprimer le Compose ou les fichiers de contrôle ;
- suppression précédée obligatoirement d'une sauvegarde ;
- déplacement en staging réversible avant validation ;
- transaction annulée en cas d'erreur ;
- provisioning appliqué après transfert, avant validation/déploiement.

### Droits conteneur

- `chmod` nécessite que l'utilisateur effectif soit propriétaire ou dispose des droits appropriés ;
- `chown` nécessite root et `CAP_CHOWN` ;
- l'accès à des répertoires autrement illisibles peut nécessiter `CAP_DAC_OVERRIDE` ;
- ces capacités ne contournent pas un montage hôte réellement en lecture seule.

**État :** ✅ implémenté, y compris suppression avec backup obligatoire et rollback.

## 11. Sauvegardes, restauration et historique

### 11.1 Sauvegardes

- archive `tar.gz` en mode `0600` ;
- manifeste des chemins et stacks ;
- contrôle d'intégrité ;
- création avant import, restauration ou suppression destructive ;
- aperçu avant restauration ;
- protection des éditeurs ouverts ;
- sauvegarde de sécurité avant d'appliquer une restauration ;
- téléchargement et suppression manuelle ;
- maximum 10 backups par binding ;
- rétention en jours, 30 par défaut ;
- cleanup de maintenance, unlink et suppression du dépôt.

**État :** ✅ implémenté.  
**Limite sécurité :** backups non chiffrés par l'application.

### 11.2 Rollback vers un commit

- historique des commits du folder link ;
- preview et comparaison ;
- sélection des fichiers ;
- sauvegarde de sécurité ;
- rollback local uniquement ;
- pause de l'automatisation pour éviter un écrasement immédiat ;
- pas de réécriture automatique de Git et pas de déploiement implicite.

**État :** ✅ implémenté.

### 11.3 Historique

Les opérations dépôt et folder link enregistrent notamment : fetch, push, import, export, conflit, résolution, suppression, pause, reprise, backup, restore, rollback, provisioning et auto-deploy.

Rétention configurable via `DOCKMAN_GIT_HISTORY_RETENTION_DAYS`, 30 jours par défaut.

**État :** ✅ implémenté.

## 12. Stockage Git et impact ressources

### 12.1 Absence de doublon permanent

Dockman ne conserve pas un clone complet avec worktree pour chaque dépôt. Il garde les objets Git et crée un worktree temporaire seulement lorsque l'export local vers Git l'exige.

Une ancienne disposition avec worktree peut être migrée vers ce stockage compact uniquement si elle est propre et ne contient pas de données non suivies ou ignorées, afin d'éviter toute perte.

Le chemin est configurable :

```text
DOCKMAN_GIT_STORAGE_PATH=/git-data
```

Ce chemin doit être absolu et ne peut pas être une racine filesystem.

**État :** ✅ implémenté.

### 12.2 Bornes

| Limite | Valeur |
|---|---:|
| Fichiers d'inventaire | 20 000 |
| Dossier de données auto-réduit | plus de 2 000 éléments non pertinents |
| Taille maximale d'un fichier | 100 MiB |
| Taille totale d'un transfert | 2 GiB |
| Taille totale des patterns | 64 KiB |
| Nombre de patterns | 1 000 |
| Comparaison texte | 2 MiB |
| Buffer de copie | 64 KiB |
| Nouvelles stacks automatiques par cycle | 10 |
| Log de déploiement conservé | 256 KiB |

Un dossier volumineux hors stack est réduit à une entrée `large_directory` et peut être exclu facilement. Un fichier illisible ou verrouillé est signalé et ignoré pour la stack concernée sans bloquer les autres stacks.

**État :** ✅ implémenté et corrigé pour les dépôts de plus de 20 000 éléments.

## 13. Interfaces Files, Monitor et Settings

### Settings / Git

- gestion des credentials et dépôts ;
- copie d'URL avec fallback lorsque Clipboard API n'est pas disponible ;
- politiques globales et par folder link ;
- sélection de stacks paginée, filtrable et multi-sélection ;
- previews paginées adaptées aux grands inventaires ;
- état, erreurs cliquables, conflits, déploiements, historique, backups et rollback ;
- pause, reprise et lancement immédiat.

### Files

- badge de synchronisation sur les stacks suivies ;
- badge gris sur les stacks éligibles non sélectionnées ;
- couleur selon état : sain, attente, conflit/erreur ou pause ;
- popup d'actions : check, preview, push, pause/reprise, historique, backups et résolution ;
- dossier parent : agrégation en lecture seule, sans redirection vers la première stack ;
- éditeur propre rechargé après sync ; éditeur sale protégé ;
- propagation d'événement stoppée afin que le clic sur le badge ne change pas d'onglet.

### Monitor

- indicateur Git sur la ligne de stack ;
- accès au détail et aux actions sans scanner le filesystem ;
- stacks imbriquées correctement indexées.

**État :** ✅ implémenté. Les anciennes boucles React et les agrégations conservant des stacks supprimées ont été corrigées.

## 14. Sécurité HTTP et filesystem

### HTTP

- API Git placée sous les routes protégées ;
- politique d'origine globale same-origin avec allowlist explicite ;
- wildcard ignoré pour les endpoints administratifs ;
- cookies d'authentification SameSite ;
- tailles de requêtes limitées ;
- header maximal 1 MiB ;
- timeout de lecture des headers et idle timeout ;
- réponses Git marquées `no-store` et `nosniff` ;
- erreurs Git nettoyées pour éviter la fuite de credentials.

### Filesystem

- racines validées et nettoyées ;
- chemins relatifs confinés ;
- traversées `..`, symlinks et fichiers spéciaux refusés selon l'opération ;
- modes restrictifs sur clé maître et backups ;
- aucune exécution du contenu synchronisé hors provisioning déclaratif ;
- worktrees temporaires supprimés après usage.

**État :** ✅ implémenté.

## 15. Corrections réalisées au fil des lots

| Correction | État |
|---|---:|
| URL sans `.git` et format `owner/repo` | ✅ |
| Message et création de branche absente | ✅ |
| Branche indépendante vide | ✅ |
| Refus dépôt+branche en doublon | ✅ |
| Copie URL sans `navigator.clipboard` | ✅ |
| Stockage Git compact sans doublon permanent | ✅ |
| Chemin Git dédié par variable d'environnement | ✅ |
| Inventaires streamés et mémoire bornée | ✅ |
| Réactivité des previews avec grands dépôts | ✅ |
| Identification/exclusion des gros dossiers | ✅ |
| Fichier illisible isolé sans bloquer les autres stacks | ✅ |
| Compose-only n'ouvre plus secrets/data hors politique | ✅ |
| Compose-only limité aux vrais noms Compose | ✅ |
| `.env.example/sample/template/dist` rétablis | ✅ |
| Inclusion explicite de fichiers de configuration en compose-only | ✅ |
| Sélection massive, recherche, filtre et pagination | ✅ |
| Sélection dès la création du link | ✅ |
| Baseline conservée au relink | ✅ |
| Auto-réconciliation initiale et pendant l'auto-sync | ✅ |
| Conflits partiels, comparaison et filtre de fenêtre corrigé | ✅ |
| Suppression Git/local avec résolution explicite | ✅ |
| État vert immédiat après résolution | ✅ |
| Nouvelle/suppression de stack reflétée dans le catalogue | ✅ |
| Push direct depuis le badge Files | ✅ |
| Cohérence des éditeurs pendant une sync | ✅ |
| Statut Git dans Files et Monitor | ✅ |
| Agrégation des dossiers parents sans clic parasite | ✅ |
| Index des stacks imbriquées et bullets corrigés | ✅ |
| État stopped distinct d'un échec | ✅ |
| Recheck automatique d'une stack rouge à commit identique | ✅ |
| Intervalle automatique sans dérive | ✅ |
| Pause/reprise avec check immédiat | ✅ |
| Déploiement indépendant par stack | ✅ |
| Logs Compose sans séquences ANSI | ✅ |
| Rollback automatique et état réconcilié | ✅ |
| Provisioning déclaratif et suppression protégée | ✅ |
| Attribution de déploiement à la stack la plus profonde | ✅ revue actuelle |
| Texte Settings cohérent avec l'auto-deploy opt-in | ✅ revue actuelle |

## 16. Tests et niveau de couverture

### Inventaire automatisé

- 38 fichiers Go dans le module Git Sync ;
- 15 fichiers de tests ;
- 153 fonctions de test ;
- couverture mesurée du package Git Sync : **68,1 %** ;
- tests avec détecteur de races exécutés sur le package ;
- tests de migrations SQLite ;
- tests d'intégration App/Config/Compose ;
- lint et build TypeScript ;
- audit des dépendances Go/npm ;
- scan de l'image multi-architecture par Trivy dans la CI.

La couverture de 68,1 % est solide pour un module contenant beaucoup d'intégrations filesystem/Git/Compose, mais elle ne remplace pas les tests réels avec GitHub, socketproxy et plusieurs filesystems.

### Scénarios d'acceptation prioritaires

#### A. Profils et grands dossiers

1. Lier un dossier contenant plusieurs stacks, un dossier `data` volumineux, un dossier `secrets` illisible et des `.env.example`.
2. En Compose only, vérifier que seuls Compose et modèles `.env` apparaissent.
3. Ajouter explicitement un `.conf` imbriqué et vérifier qu'il apparaît.
4. Vérifier qu'aucune lecture du reste de `secrets/data` n'échoue.
5. Passer en Compose + configuration puis All files et vérifier les différences attendues.

#### B. Synchronisation bidirectionnelle

1. Modifier un fichier local et constater `Local changes waiting`.
2. Preview puis push depuis Settings et depuis le badge Files.
3. Modifier un autre fichier sur Git puis importer.
4. Vérifier la mise à jour d'un éditeur propre.
5. Répéter avec un éditeur sale et vérifier le blocage ciblé.

#### C. Conflits

1. Modifier trois fichiers des deux côtés depuis la même baseline.
2. Résoudre un seul conflit en faveur de Git.
3. Résoudre le deuxième en faveur de Dockman.
4. Laisser le troisième en attente et vérifier que le statut reste conflit.
5. Résoudre le dernier et vérifier le check automatique puis l'état vert.

#### D. Suppressions

1. Supprimer un fichier sur Git : tester restore Git, archive locale et suppression locale protégée.
2. Supprimer un fichier local : tester restauration Git, suppression Git confirmée et exclusion.
3. Répéter avec une stack entière et vérifier qu'aucun volume n'est supprimé.
4. Vérifier l'état vert immédiat après chaque résolution.

#### E. Automatisation

1. Régler 5 minutes et vérifier les échéances réelles.
2. Ajouter une stack valide et une stack volontairement invalide dans le même commit.
3. Vérifier que la valide est traitée malgré l'échec de l'autre.
4. Mettre en pause le folder link puis une seule stack.
5. Reprendre et vérifier le check complet immédiat.
6. Corriger une stack rouge sans nouveau commit Git et vérifier le recheck automatique.

#### F. Déploiement racine et sous-stacks

1. Sélectionner `compose.yml`, `apps/alpha/compose.yml` et `apps/beta/compose.yml` en auto-deploy.
2. Modifier uniquement `apps/alpha/config.yml`.
3. Vérifier que seule `apps/alpha` est redéployée.
4. Modifier un fichier appartenant à la stack racine.
5. Vérifier que seule la stack racine est redéployée.

#### G. Rollback et provisioning

1. Déployer une version saine avec healthcheck.
2. Pousser une configuration faisant échouer le service.
3. Vérifier la restauration des fichiers et le redéploiement précédent.
4. Tester `mkdir`, `chmod`, `chown` avec les capacités attendues.
5. Tester une suppression provisioning et vérifier la présence du backup.
6. Provoquer une erreur après suppression et vérifier sa restauration.

#### H. Ressources

1. Laisser Dockman sans interaction pendant 15 minutes hors échéance Git.
2. Relever CPU/RAM toutes les secondes.
3. Exécuter une preview de 20 000 éléments puis une sync.
4. Vérifier le retour de la mémoire vers son niveau habituel.
5. Simuler un dépôt injoignable et vérifier le timeout et le passage aux autres links.

## 17. Limites et travaux futurs

### Priorité élevée

1. **Support GitLab/Bitbucket/générique :** normalisation d'URL, API de création, validation SSH et tests provider.
2. **Chiffrement des backups :** intégration d'une clé distincte ou d'un backend SOPS/age/Kopia/Restic.
3. **Signature des commits :** SSH ou GPG pour attester les commits automatisés.

### Priorité moyenne

1. Webhooks GitHub pour réduire la latence sans augmenter le polling.
2. Politique de suppression configurable preserve/archive/mirror avec garde-fous.
3. Branche configurable par folder link.
4. Parallélisme réseau borné entre dépôts afin qu'un remote lent ne retarde pas les autres.

### Maintenabilité

Deux fichiers sont devenus des points chauds :

- `core/internal/gitsync/binding.go` dépasse 3 000 lignes ;
- `ui/src/pages/settings/tab-git.tsx` dépasse 1 400 lignes.

Ils devraient être découpés par responsabilité lors d'un lot de refactoring sans changement fonctionnel : inventory/policy/transfer pour le backend et repositories/bindings/preview/recovery pour l'UI.

## 18. Verdict

Le Git Sync de Dockman est prêt pour une validation d'intégration complète et structurée. Les règles critiques sont appliquées :

- non-destructif par défaut ;
- aucune lecture inutile hors politique ;
- secrets protégés ;
- conflits et suppressions explicites ;
- stacks indépendantes ;
- déploiement et rollback opt-in ;
- provisioning sans shell ;
- stockage et ressources bornés ;
- statuts exploitables dans les vues quotidiennes.

La fonctionnalité ne doit pas encore être présentée comme universelle : elle reste GitHub-only, n'envoie pas automatiquement Dockman vers Git et ne chiffre pas ses backups. Ces limites sont documentées et ne remettent pas en cause les scénarios GitHub actuellement ciblés.
