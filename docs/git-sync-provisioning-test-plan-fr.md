# Cahier de test — provisionnement déclaratif Git

## Objectif et périmètre

Un fichier `provision.yml` ou `provision.yaml`, placé dans le même dossier Git que le fichier Compose, permet de créer des dossiers et d'appliquer des droits après la synchronisation Git et avant la validation puis le déploiement contrôlé.

Le manifeste est un fichier de contrôle réservé : il est suivi par la synchronisation, mais n'est jamais copié dans le dossier live de la stack. Il n'exécute aucun shell et ne peut agir qu'à l'intérieur du dossier de la stack concernée. Les suppressions sont possibles uniquement par une section `remove` explicite et après création réussie d'un backup complet.

## Préparation

Activer pour la stack de test :

- la synchronisation automatique Git vers Dockman ;
- l'auto-déploiement ;
- le rollback automatique.

Créer dans le dépôt :

```yaml
# demo/compose.yml
services:
  demo:
    image: alpine:3.23
    command: ["sh", "-c", "test -r /config/app.yml && sleep infinity"]
    volumes:
      - ./config:/config
```

```yaml
# demo/config/app.yml
enabled: true
```

```yaml
# demo/provision.yml
version: 1

directories:
  - path: data/cache
    mode: "0750"

permissions:
  - path: config/app.yml
    mode: "0640"
```

Les modes doivent rester des chaînes octales entre guillemets. Pour changer le propriétaire, ajouter ensemble `uid` et `gid` :

```yaml
permissions:
  - path: config/app.yml
    mode: "0640"
    uid: 1000
    gid: 1000
```

## Test 1 — synchronisation et ordre d'exécution

1. Committer puis pousser les trois fichiers.
2. Lancer `Sync now`, ou attendre le prochain passage automatique.
3. Vérifier que la stack est déployée correctement.
4. Sur l'hôte, vérifier les modes de `demo/data/cache` et `demo/config/app.yml`.
5. Vérifier que `demo/provision.yml` n'existe pas dans le dossier live Dockman.
6. Ouvrir l'historique de la stack.

Résultat attendu : les fichiers sont copiés avant l'application des droits ; le dossier est en `0750`, le fichier en `0640`, le manifeste reste uniquement dans Git et une activité `stack_provision` est enregistrée.

## Test 2 — idempotence et absence de boucle

1. Relancer immédiatement `Sync now` sans modifier Git.
2. Attendre un second cycle automatique.

Résultat attendu : aucun nouveau changement, aucun nouveau déploiement et aucune boucle liée à l'absence volontaire du manifeste dans le dossier live.

## Test 3 — modification du seul manifeste

1. Modifier uniquement le mode du fichier dans `provision.yml`, par exemple `"0600"`.
2. Committer puis pousser.
3. Lancer la synchronisation.
4. Vérifier le mode local puis relancer `Sync now`.

Résultat attendu : le changement du manifeste déclenche une seule application et un seul déploiement contrôlé. Le passage suivant est à jour.

## Test 4 — rollback des droits et dossiers

1. Relever le mode actuel de `config/app.yml`.
2. Dans le même commit Git, demander un autre mode, créer un nouveau dossier via `directories`, puis conserver un YAML lisible mais rendre volontairement le modèle Compose invalide pour Docker (service sans image ni build, par exemple).
3. Pousser et lancer la synchronisation.
4. Consulter le détail de l'échec et l'historique.
5. Vérifier le système de fichiers local.

Résultat attendu : la validation échoue avant toute modification Docker ; les droits/propriétaires antérieurs sont restaurés, les dossiers créés uniquement par le provisionnement sont retirés s'ils sont vides, les fichiers synchronisés sont restaurés par le backup et l'état indique `rolled_back`.

Si un dossier créé est devenu non vide entre-temps, Dockman refuse de l'effacer et signale `rollback_failed` plutôt que de supprimer des données.

## Test 5 — manifeste invalide

Tester séparément :

- les deux fichiers `provision.yml` et `provision.yaml` présents ensemble ;
- une clé YAML inconnue ;
- un mode `"4755"` ;
- `uid` sans `gid` ;
- plus de 128 opérations ;
- un manifeste supérieur à 64 Kio.

Résultat attendu : le provisionnement est refusé avec un message précis, le déploiement ne démarre pas et aucune mutation de droits ne subsiste.

## Test 6 — confinement

Tester séparément les chemins `/tmp/test`, `../test`, `.git/config` et un chemin passant par un lien symbolique créé dans la stack.

Résultat attendu : chaque tentative est refusée. Aucun fichier ou dossier extérieur à la stack n'est créé ou modifié.

## Test 7 — suppression du manifeste

1. Supprimer `provision.yml` dans Git, committer et pousser.
2. Synchroniser deux fois.

Résultat attendu : la suppression du fichier de contrôle est mémorisée sans tenter de supprimer un fichier local inexistant. Si le commit ne contient que cette suppression, la stack n'est pas redéployée. La synchronisation suivante est verte et les prochains déploiements n'appliquent plus ce manifeste. Les permissions déjà appliquées ne sont pas modifiées implicitement.

## Test 8 — hôte SSH

Rejouer les tests 1, 2 et 4 sur une stack gérée via SSH/SFTP.

Résultat attendu : même comportement qu'en local, y compris `chmod`, `chown`, détection des liens symboliques et rollback.

## Test 9 — suppression protégée

Créer un fichier et un dossier contenant un fichier ainsi qu'un sous-dossier vide, puis ajouter :

```yaml
remove:
  - path: obsolete.conf
    type: file
  - path: old-data
    type: directory
    recursive: true
```

Résultat attendu : un backup `pre_provision_delete` est créé avant toute mutation, puis les cibles sont supprimées après succès. En provoquant un échec de déploiement avec le rollback automatique actif, leur contenu, leurs dossiers vides et leurs métadonnées sont restaurés exactement. Un dossier non vide sans `recursive: true`, un type incorrect, un lien symbolique, un fichier spécial, un chemin protégé ou hors stack est refusé sans suppression.
