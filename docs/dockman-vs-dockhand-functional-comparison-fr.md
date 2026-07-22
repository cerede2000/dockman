# Dockman vs Dockhand — comparaison fonctionnelle

État vérifié le 22 juillet 2026 sur :

- Dockman : branche `feat/git-sync-automation`, issue du lot 4 validé et incluant le lot 5 ;
- Dockhand : commit `9f09d60a83814bb794af9bc9936fae75ffc27474` du dépôt `Finsys/dockhand`.

Cette comparaison décrit les fonctions présentes dans le code. Une case « partiel » indique une différence de périmètre ou de philosophie, pas nécessairement un défaut.

## Synthèse

Dockman couvre maintenant très bien l'administration Docker quotidienne, avec une empreinte réduite, un navigateur de fichiers particulièrement tolérant aux images minimales/read-only, et une synchronisation Git bidirectionnelle plus prudente et plus explicite que Dockhand. Dockhand conserve une avance nette sur les fonctions de plateforme : audit utilisateur, RBAC, API tokens, alertes/notifications, planification générique, scanner CVE interactif, templates, éditeur de graphes et déploiement Git automatisé par cron/webhook.

Le lot 5 Dockman automatise volontairement **l'import Git vers les fichiers de stack sans déployer**. Dockhand automatise directement le sync puis le `compose up`. C'est le principal écart restant sur Git, mais aussi une différence de niveau de risque.

## Plateforme, hôtes et sécurité d'accès

| Fonction | Dockman | Dockhand | Observation |
|---|---:|---:|---|
| Hôte Docker local | Oui | Oui | Support natif. |
| Multi-hôtes | Oui | Oui | Dockman : Docker distant et SSH ; Dockhand : socket, TCP et agent Hawser. |
| Fichiers de stacks sur hôte SSH | Oui | Partiel | Dockman utilise la même abstraction confinée local/SFTP. |
| Docker Socket Proxy | Oui | Oui | Dockman a été testé dans ce mode ; les routes nécessaires doivent être permises. |
| Authentification locale | Oui | Oui | Sessions persistantes. |
| OIDC/SSO | Oui | Oui | Configurable dans les deux produits. |
| MFA/TOTP | Non | Oui | Avantage Dockhand. |
| RBAC multi-utilisateurs | Non | Oui/Enterprise | Dockman reste mono-administrateur. |
| API tokens | Non | Oui | Avantage Dockhand. |
| Journal d'audit utilisateur | Non | Oui | Dockman journalise les opérations Git, pas toutes les actions utilisateur. |
| Contrôle d'origine HTTP | Oui | Oui | Dockman applique same-origin et une allowlist explicite. |
| Limites HTTP/upload | Oui | Oui | Dockman distingue corps standard et upload. |
| Chiffrement des secrets Git | Oui | Oui | AES-GCM ; Dockman accepte une clé montée comme secret et ne restitue jamais le secret par API. |
| Exec dans son propre conteneur | Bloqué par défaut | Pas de garde équivalente identifiée | Dockman exige `DOCKMAN_ALLOW_SELF_EXEC=true`. |

## Supervision et conteneurs

| Fonction | Dockman | Dockhand | Observation |
|---|---:|---:|---|
| Vue temps réel CPU/RAM/réseau/disque | Oui | Oui | Graphes et valeurs. |
| Vue par stack et liste plate | Oui | Oui | Dockman permet aussi de masquer les graphes. |
| Filtres de statut cumulables | Oui | Oui | Dockman accepte la multisélection avec Ctrl. |
| Start/stop/restart/pause/remove | Oui | Oui | Actions individuelles et groupées selon la vue. |
| Blocage des actions concurrentes | Oui | Oui | Dockman bloque aussi les actions conteneur pendant une action de stack. |
| Mise à jour/recréation d'un conteneur | Oui | Oui | Rollback technique en cas d'échec dans les deux projets. |
| Création/édition d'un conteneur brut | Non | Oui | Avantage Dockhand. |
| Renommage d'un conteneur | Non | Oui | Avantage Dockhand. |
| Auto-update planifié des conteneurs | Non | Oui | Avantage Dockhand. |
| Détails complets du conteneur | Oui | Oui | État, image/SHA, ressources, sécurité, health, mounts, env, labels, réseaux, ports. |
| Processus avec refresh | Oui | Oui | Présent dans le panneau de détails. |
| Logs temps réel | Oui | Oui | Recherche/options/copie et bandeau fixe. |
| Exec multi-shell/multi-user | Oui | Oui | Détection de shells et contexte utilisateur. |
| Inspect JSON coloré/copiable | Oui | Oui | Présent dans les deux. |
| Couches d'image dans le détail conteneur | Non | Oui | Dockhand intègre la vue des layers. |

## Stacks Compose

| Fonction | Dockman | Dockhand | Observation |
|---|---:|---:|---|
| Découverte/liste des stacks | Oui | Oui | Multi-hôtes. |
| Up/down/start/stop/restart/redeploy/update | Oui | Oui | Dockman remet immédiatement les stats transitoires à `-`. |
| Éditeur YAML | Oui | Oui | Dockman ajoute validation Compose/LSP et navigation de fichiers. |
| Gestion `.env` | Oui | Oui | Approches différentes ; Dockhand distingue les secrets stockés en base. |
| Logs/terminal par stack | Oui | Oui | Présent dans les deux. |
| Éditeur graphique de stack | Non | Oui | Avantage Dockhand. |
| Graphe de topologie réseau | Non | Oui | Avantage Dockhand. |
| Templates/catalogues Compose | Partiel | Oui | Dockman dispose d'outils d'édition/génération, pas d'un catalogue multi-source. |

## Images, volumes, réseaux et maintenance

| Fonction | Dockman | Dockhand | Observation |
|---|---:|---:|---|
| Liste/inspect/suppression/prune images | Oui | Oui | Présent dans les deux. |
| Détection et application d'update image | Oui | Oui | Depuis monitor/détails dans Dockman. |
| Pull/push registre depuis l'UI | Partiel | Oui | Dockhand est plus complet sur les registres. |
| Scan CVE interactif par image | Non | Oui | Dockhand intègre Trivy/Grype ; Dockman scanne sa propre image en CI avec une gate HIGH/CRITICAL corrigeable. |
| Analyse des couches d'image | Non | Oui | Avantage Dockhand. |
| Volumes : liste/inspect/création/suppression | Oui | Oui | Présent dans les deux. |
| Navigateur de volume RW | Oui | Oui | Upload, création, renommage, chmod/chown, suppression et download. |
| Clone de volume | Non | Oui | Avantage Dockhand. |
| Réseaux : liste/inspect/prune | Oui | Oui | Présent dans les deux. |
| Connecter/déconnecter un réseau | Oui | Oui | Disponible dans le détail conteneur Dockman. |
| Cleaner planifié | Oui | Oui | Dockman conserve un historique court ; Dockhand expose un ordonnanceur plus général. |
| Notifications/alertes | Non | Oui | Avantage Dockhand. |

## Navigateurs de fichiers

| Fonction | Dockman | Dockhand | Observation |
|---|---:|---:|---|
| Fichiers du conteneur | Oui | Oui | Navigation, tri, hidden files, upload/download, création et édition. |
| Dossiers en TAR/ZIP | Oui | Oui | Dockman propose les deux formats. |
| chmod et chown récursifs | Oui | Oui | Présent dans Dockman. |
| Conteneur sans `ls`/`find`/shell | Oui | Partiel | Dockman utilise un helper injecté, puis un fallback archive borné. |
| Rootfs read-only avec bind RW | Oui | Partiel | Dockman détecte la portée RW par chemin et réactive seulement les actions sûres. |
| Entrées `/dev` et `/proc` non archivables | Oui | Partiel | Dockman transforme les 404 Docker archive en entrées non disponibles sans casser la navigation. |
| Navigateur de volumes | Oui | Oui | Même composant et mêmes garde-fous côté Dockman. |
| Confinement des chemins | Oui | Oui | Dockman utilise `openat`/`os.Root` côté local et confinement SFTP côté distant. |

## Git et GitOps

| Fonction | Dockman lot 5 | Dockhand | Observation |
|---|---:|---:|---|
| Multi-repositories publics/privés | Oui | Oui | HTTPS token et SSH. |
| Création d'un dépôt GitHub | Oui | Non identifié | Dockman peut créer puis cloner un dépôt via l'API GitHub. |
| Repo utilisé comme template sans stack prod | Oui | Oui | Le dépôt peut exister sans lien/déploiement. |
| Un dossier complet de stacks vers un repo/dossier | Oui | Partiel | Dockman conserve automatiquement toute l'arborescence sous un seul lien. |
| Import Git → Dockman manuel | Oui | Oui | Dockman prévisualise et sauvegarde avant écriture. |
| Export Dockman → Git avec commit/push | Oui | Non | Avantage Dockman. |
| Profils de fichiers/inclusions/exclusions | Oui | Partiel | Dockman filtre types, secrets, spéciaux, taille et `.dockmanignore`, avec édition en masse. |
| Transfert borné/streamé | Oui | Oui | Dockman : buffer 64 KiB, 20 000 fichiers, 2 GiB sélectionnés, 100 MiB par fichier. Dockhand utilise notamment le clone blobless. |
| Baseline sans copie des contenus | Oui | Oui/manifest | Dockman stocke uniquement les SHA par chemin ; Dockhand conserve un manifeste des fichiers déployés. |
| Détection de conflit bidirectionnelle | Oui | Non équivalent | Dockman compare baseline/source/destination avant écrasement. |
| Diff visuel par fichier | Oui | Non équivalent | Monaco côte à côte dans Dockman. |
| Résolution partielle des conflits | Oui | Non équivalent | Un fichier peut être décidé, les autres restent en attente. |
| Non-suppression par défaut | Oui | Non | Dockhand propage certaines suppressions avec manifeste et garde-fous ; Dockman conserve toujours le fichier destination. |
| Auto-sync périodique opt-in | Oui | Oui | Dockman : fréquence 5 min–24 h et scan seulement sur nouveau commit ; Dockhand : cron. |
| Auto-sync sans déploiement | Oui | Non | Choix de sécurité Dockman : backup/import seulement. |
| Auto-déploiement/recompose | Non | Oui | Dockhand peut sync puis `compose up`, build, repull et force-recreate. Lot suivant Dockman. |
| Webhooks GitHub/GitLab | Non | Oui | Dockhand vérifie signature/secret. Lot futur Dockman. |
| Rollback applicatif vers un commit | Non | Non identifié | Aucun workflow complet de rollback Git n'a été identifié dans Dockhand ; à concevoir dans Dockman. |
| Nettoyage/réécriture de l'historique Git | Non | Non | Une réécriture automatique serait risquée ; à séparer d'un simple nettoyage du clone. |

## Image, dépendances et livraison

| Fonction | Dockman | Dockhand | Observation |
|---|---:|---:|---|
| Image multi-architecture | Oui | Oui | amd64/arm64. |
| Base minimale et épinglée | Oui | Oui | Dockman : Alpine épinglée par digest ; Dockhand : couche Wolfi/apko déclarative. |
| SBOM/provenance | Oui | À confirmer | Workflow Dockman publie SBOM et provenance BuildKit. |
| Signature d'image | Oui | À confirmer | Dockman signe le manifeste multi-arch avec Cosign keyless. |
| Gate vulnérabilités de build | Oui | À confirmer | Dockman bloque les HIGH/CRITICAL corrigeables et conserve le rapport complet Trivy par architecture. |
| Rétention des images de test | Oui | À confirmer | Dockman conserve les trois dernières images `integration`. |

## Priorités recommandées après validation du lot 5

1. Déploiement contrôlé après sync, opt-in séparé, avec validation Compose, dry-run, verrou de stack et journal complet.
2. Rollback vers le commit et le backup précédents, testé sur échec de recompose et sur healthcheck.
3. Webhook signé, avec anti-rejeu, filtrage de branche et mise en file par stack.
4. Vue globale Git : sync/check de tous les liens, conflits et erreurs sans ouvrir chaque ligne.
5. Scan CVE interactif des images, qui représente le plus gros écart sécurité visible avec Dockhand.
6. Audit global et rôles si Dockman évolue vers un usage réellement multi-utilisateurs.
