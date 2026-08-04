# Lot 3 — notifications SMTP des scans d’images

## Objectif et périmètre

Ce lot ajoute des notifications SMTP aux scans planifiés de la vue **Updates**. Il reste strictement en lecture seule : aucun conteneur et aucune stack ne sont mis à jour, recréés ou redémarrés automatiquement.

Dockman envoie un message groupé uniquement lorsque l’ensemble des mises à jour disponibles ou des erreurs de scan change. Les scans manuels n’envoient aucun message. Le bouton **Send test** est la seule exception manuelle explicite.

## Préparation recommandée de la clé

Pour un premier test, Dockman peut générer automatiquement `/config/notifications/master.key`. Cette clé doit rester sauvegardée avec `/config`.

Pour la production, créer et monter une clé dédiée :

```sh
openssl rand -base64 32 > dockman_notification_key
chmod 600 dockman_notification_key
```

Puis monter ce fichier en lecture seule, par exemple dans `/run/secrets/dockman_notification_key`, et définir :

```yaml
environment:
  DOCKMAN_NOTIFICATION_MASTER_KEY_FILE: /run/secrets/dockman_notification_key
volumes:
  - ./dockman_notification_key:/run/secrets/dockman_notification_key:ro
```

La perte ou le remplacement de cette clé rend le mot de passe SMTP déjà enregistré indéchiffrable.

## 1. Migration et démarrage

1. Déployer l’image du lot 3 sans modifier le reste de la configuration.
2. Vérifier que Dockman démarre normalement et que les vues **Files**, **Monitor** et **Updates** restent accessibles.
3. Ouvrir **Updates** puis **SMTP**.

Attendu : aucune erreur de migration, configuration SMTP désactivée par défaut, aucun changement sur les conteneurs.

## 2. Configuration et chiffrement du secret

1. Saisir le serveur, le port, la sécurité, l’utilisateur, le mot de passe, l’expéditeur et les destinataires.
2. Utiliser `STARTTLS` pour le port 587 ou `TLS / SMTPS` pour le port 465 selon le fournisseur.
3. Enregistrer.
4. Fermer puis rouvrir la fenêtre.

Attendu : la configuration est conservée, l’interface indique qu’un mot de passe existe mais ne le réaffiche jamais. Laisser le champ vide puis enregistrer doit conserver le mot de passe existant.

## 3. Test SMTP explicite

1. Cliquer sur **Send test**.
2. Vérifier la réception du message.
3. Vérifier la ligne correspondante dans **Recent deliveries**.

Attendu : un seul message de test est envoyé et l’historique affiche `sent`. Une erreur SMTP doit être lisible dans cet historique sans affecter Dockman.

### Relais STARTTLS avec une autorité privée

Pour un relais utilisant une CA interne, monter la CA au format PEM à l’emplacement reconnu automatiquement :

```yaml
services:
  dockman:
    volumes:
      - /server/certs/smtp-ca.crt:/etc/ssl/certs/smtp-ca.crt:ro
```

Puis recréer Dockman et relancer **Send test** en mode `STARTTLS`. La CA privée est ajoutée au magasin système : les autorités publiques continuent donc de fonctionner et la vérification du nom du serveur reste obligatoire.

Un autre chemin peut être déclaré explicitement :

```yaml
environment:
  DOCKMAN_SMTP_CA_FILE: /run/secrets/internal-smtp-ca.crt
volumes:
  - /server/certs/smtp-ca.crt:/run/secrets/internal-smtp-ca.crt:ro
```

Si `DOCKMAN_SMTP_CA_FILE` est défini mais absent, illisible, supérieur à 1 Mio ou sans certificat PEM valide, l'envoi est refusé avec un message explicite. Dockman ne bascule jamais vers `InsecureSkipVerify`.

## 4. Notification de mise à jour disponible

1. Activer **Notify available updates**.
2. Inscrire au moins un conteneur dont une nouvelle image existe.
3. Utiliser temporairement une planification sûre, par exemple `*/15 * * * *`.
4. Attendre le scan planifié.

Attendu : un message groupé décrit les conteneurs concernés. Aucun conteneur n’est modifié.

## 5. Anti-spam et réapparition

1. Laisser le scan planifié suivant s’exécuter sans changer les images.
2. Mettre ensuite l’image à jour manuellement afin de faire disparaître l’événement.
3. Faire réapparaître ultérieurement une mise à jour pour la même image.

Attendu : aucun doublon lors du deuxième scan inchangé. Après disparition puis réapparition, un nouveau message est envoyé.

## 6. Erreurs de scan

1. Activer **Notify scan errors**.
2. Utiliser temporairement une référence d’image inaccessible ou provoquer une indisponibilité du registre sur un conteneur de test.
3. Attendre le scan planifié.

Attendu : le mail groupe les erreurs utiles. Une image purement locale ignorée n’est pas présentée comme une erreur.

## 7. Préférences indépendantes

1. Désactiver les notifications de mises à jour et laisser celles des erreurs actives.
2. Vérifier qu’un mail d’erreur ne contient aucune mise à jour disponible.
3. Inverser les deux options et recommencer.

Attendu : chaque catégorie est réellement indépendante.

## 8. Scan manuel et absence d’action automatique

1. Cliquer plusieurs fois sur **Scan enrolled**.
2. Contrôler les mails, l’historique SMTP et les conteneurs.

Attendu : aucun mail automatique n’est envoyé par les scans manuels et aucun conteneur n’est recréé, redémarré ou mis à jour.

## 9. Panne SMTP sans régression du scan

1. Enregistrer volontairement un port SMTP incorrect.
2. Attendre un scan planifié contenant un événement notifiable.
3. Vérifier l’historique des scans et celui des livraisons.
4. Corriger le port et attendre ou provoquer la prochaine occurrence planifiée.

Attendu : le scan d’images reste terminé et exploitable, la livraison est marquée `failed`, puis le même événement est retenté après correction au lieu d’être perdu.

## 10. Persistance après redémarrage

1. Redémarrer le conteneur Dockman avec la même clé et le même `/config`.
2. Rouvrir la configuration SMTP.
3. Envoyer un nouveau message de test.

Attendu : la configuration et l’historique sont présents, le mot de passe reste masqué et le test fonctionne sans le ressaisir.

## Validation finale

- aucune régression Files/Monitor/Updates ;
- aucun secret SMTP retourné par l’API ou affiché ;
- aucun mail en double pour un état inchangé ;
- isolation correcte entre plusieurs créneaux planifiés d’un même hôte ;
- panne SMTP sans panne du scan ;
- aucune mise à jour automatique de conteneur dans ce lot.
