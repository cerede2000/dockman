# Secrets — cahier de test du lot 3 SOPS/age

## 1. Préparation indépendante

1. Générer l'identité directement dans le volume persistant de Dockman :

   ```console
   docker exec dockman dockman-age-keygen
   ```

2. Vérifier que le dossier est en `0700`, le fichier en `0600`, avec le même
   propriétaire que `PUID:PGID`, puis conserver le destinataire public `age1...`.
3. Sauvegarder le fichier privé en dehors de l'hôte sans le placer dans Git.
4. Définir `DOCKMAN_SOPS_AGE_KEY_FILE=/config/secrets/dockman-sops-age-key.txt`
   et `DOCKMAN_SOPS_AGE_RECIPIENT`, puis recréer Dockman.

Attendu : **Settings → Secrets** affiche `SOPS/age · ready`. La clé privée ne
doit apparaître ni dans l'interface, ni dans les logs, ni dans `docker inspect`.

## 2. Chiffrement et vérification

1. Charger une stack de test contenant deux secrets runtime sans valeur de production.
2. Choisir **Encrypt runtime**, saisir `CONFIRM` et valider.
3. Ouvrir `secrets.sops.yaml` dans la vue Files.

Attendu : les deux noms sont visibles, aucune valeur claire ne l'est, le bloc
SOPS contient le destinataire age et l'opération annonce deux secrets. Une
identité et un destinataire volontairement incompatibles doivent empêcher le
remplacement du fichier source.

## 3. Matérialisation et permissions

1. Donner à un secret runtime le mode `0444` et noter sa valeur.
2. Modifier ce secret dans le fichier SOPS avec la CLI officielle, puis pousser
   ou copier le fichier chiffré dans la stack.
3. Cliquer **Materialize source**, saisir `CONFIRM` et valider.
4. Révéler la valeur et contrôler le mode du fichier.

Attendu : la nouvelle valeur est présente et le mode reste `0444`. Un secret
runtime absent du document SOPS reste intact. Aucun secret n'est supprimé.

## 4. Git et frontière plaintext

1. Utiliser un folder link en profil **Docker Compose only**.
2. Prévisualiser Stack → Git.

Attendu : `secrets.sops.yaml` est transférable automatiquement à côté du
Compose, tandis que `.secrets/` et `.secrets/.history/` sont toujours absents.

## 5. Reprise sans Dockman

1. Arrêter Dockman.
2. Déchiffrer manuellement avec :

   ```console
   SOPS_AGE_KEY_FILE=./dockman-sops-age-key.txt \
     sops decrypt --input-type yaml --output-type json secrets.sops.yaml
   ```

3. Vérifier que `docker compose up -d` fonctionne avec les fichiers déjà
   matérialisés sous `.secrets/`.

Attendu : ni le déchiffrement ni le démarrage Compose ne dépendent du processus
Dockman. La sauvegarde de l'identité age reste indispensable.

## 6. Multi-hôtes et overhead

1. Répéter export et matérialisation sur une stack SSH de test.
2. Vérifier que le ciphertext et `.secrets` restent sur le host sélectionné.
3. Laisser la page ouverte sans action et contrôler réseau/CPU.

Attendu : aucune copie de clé sur le host distant, aucun polling SOPS, aucun
processus SOPS résident et aucun overhead au repos.
