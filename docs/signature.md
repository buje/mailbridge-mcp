# Signature par compte

Chaque compte peut avoir une signature texte + HTML, avec images embarquées par
Content-ID (CID). Elle est ajoutée automatiquement par `send_email`,
`save_draft` et `reply_email`, entre le texte et le message cité.

## Configuration

Dans `~/.config/mailbridge/accounts.json`, sur le compte :

```json
"signature": {
  "text_file": "signatures/squirrel.txt",
  "html_file": "signatures/squirrel.html",
  "images": [
    { "cid": "logo", "path": "signatures/logo.png" }
  ]
}
```

- `text` / `html` : contenu en ligne ; `text_file` / `html_file` : fichiers
  (chemins relatifs à `~/.config/mailbridge`, `~/` accepté). Relus à chaque
  envoi : une modification s'applique sans redémarrer le serveur.
- `text` absent : dérivé du HTML. `-- ` en tête est retiré (ajouté par mailbridge).
- `images` : `cid` = nom utilisé dans le HTML (`<img src="cid:logo">`),
  `content_type` détecté si absent. PNG, JPEG ou GIF uniquement, 1 Mo max.
  Les images non référencées par le HTML ne sont pas jointes.
- `disabled: true` : désactive la signature du compte.

`mailbridge-mcp setup` conserve la signature d'un compte existant.

## Importer depuis Apple Mail

Plutôt que d'écrire le HTML à la main, la signature peut être reprise de
l'app Mail :

```bash
mailbridge-mcp signature            # choix du compte dans une liste
mailbridge-mcp signature pro        # ou ID du compte
```

`mailbridge-mcp setup` propose aussi l'import à l'ajout d'un compte.

- Les signatures sont lues dans le dossier actif de Mail
  (`~/Library/Mail/V*/MailData/Signatures`). La version la plus récente est
  prise, et les copies iCloud ou du conteneur Mail, parfois périmées, sont
  ignorées.
- `AllSignatures.plist` donne les signatures existantes : les fichiers laissés
  par des signatures supprimées dans Mail ne sont pas proposés.
- `AccountsMap.plist` indique la signature que Mail utilise pour chaque
  adresse : elle est présélectionnée (`<- used by … in Mail`), on valide avec
  Entrée. À défaut, l'outil suggère celle qui contient l'adresse ou son domaine.
- La signature choisie est convertie dans le format ci-dessus :
  `signatures/<compte>.html`, et chaque image embarquée enregistrée sous
  `signatures/<compte>-sig<n>.png`, déclarée dans `images` (`cid` `sig1`,
  `sig2`…). Les balises Apple `<object type="application/x-apple-msg-attachment">`
  deviennent des `<img>`. Les images hébergées en ligne (`https://…`) restent
  des liens. Les images refusées par le chargeur (autre format que PNG, JPEG
  ou GIF, plus de 1 Mo) sont retirées du HTML pour que l'envoi n'échoue pas.
- L'import est ponctuel : le serveur ne lit jamais `~/Library`. Pour reprendre
  une signature modifiée dans Mail, relancer la commande.

macOS protège les données de Mail application par application : la commande
lit directement le dossier `Signatures` (lister `~/Library/Mail` est
interdit). Si l'accès est refusé, la lancer depuis une app autorisée (Terminal)
ou autoriser l'app courante dans *Réglages Système › Confidentialité et
sécurité › Accès complet au disque*. Ce n'est utile que pour l'import.

Pour créer une signature compatible Apple Mail, Gmail et Outlook :
[générateur de signature email gratuit de Senzu Tech](https://senzu.tech/tools/signature-email-generator/)
(modèles email-safe, images hébergées, réseaux sociaux, bouton d'action). On
l'ajoute dans Mail, puis on lance `mailbridge-mcp signature`.

## Par message

| Paramètre | Effet |
|---|---|
| `signature: false` | aucune signature |
| `signature_text` | remplace la version texte (et la signature entière) |
| `signature_html` | remplace la version HTML ; peut utiliser les `cid:` du compte |

## Structure MIME produite

```
multipart/mixed                 (si pièces jointes)
├─ multipart/related            (si images)
│  ├─ multipart/alternative     (si signature HTML)
│  │  ├─ text/plain             corps + "-- " + signature + citation "> "
│  │  └─ text/html              corps + signature + <blockquote>
│  └─ image/png                 Content-ID: <logo.<aléa>@domaine>, inline
└─ application/pdf ...          attachment
```

Sans signature HTML, le message reste en `text/plain` seul. Les Content-ID sont
uniques par message (`nom.<hex>@domaine-expéditeur`) : pas de collision avec une
image citée d'un message précédent. Aucune image externe ni `data:` base64 dans
le HTML.

## Brouillons et doublons

- `save_draft` / `reply_email` enregistrent le brouillon signé.
- `send_draft` envoie le brouillon tel quel : jamais de seconde signature.
- Si le corps contient déjà le texte de la signature (espaces ignorés), elle
  n'est pas ajoutée. Pour une réponse, seul le nouveau texte est vérifié, pas
  la citation.

## HTML recommandé

Styles en ligne uniquement, mise en page en `<table>`, `width`/`height` en
attributs sur `<img>`, `alt` renseigné, polices système (Arial, Helvetica).
Exemple :

```html
<table cellpadding="0" cellspacing="0" border="0" role="presentation">
  <tr>
    <td style="padding-right:12px;vertical-align:middle;">
      <img src="cid:logo" width="120" height="40" alt="Squirrel" style="display:block;border:0;">
    </td>
    <td style="font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:1.4;color:#333333;">
      <strong>Edouard CLAUDE</strong><br>Squirrel<br>
      <a href="mailto:edouard@squirrel.fr" style="color:#1a73e8;text-decoration:none;">edouard@squirrel.fr</a>
    </td>
  </tr>
</table>
```

Un document HTML complet est accepté : seul le contenu de `<body>` est gardé
(`<head>` et `<style>` sont retirés).

## Limites de compatibilité

Vérifié automatiquement : arborescence MIME relue par go-message et par le
parseur `email` de Python, correspondance `cid:` ↔ `Content-ID`, encodage
quoted-printable (lignes ≤ 998). Le rendu dans les clients ci-dessous n'a pas
été testé par envoi réel ; ce sont les comportements connus de chaque client.

| Client | Comportement / limite |
|---|---|
| Gmail (web, Android, iOS) | Images CID affichées en ligne. `<style>` ignoré dans certains cas → styles en ligne. Message > 102 Ko de HTML tronqué (« Message tronqué ») : dans un long fil, la signature peut disparaître de l'aperçu. |
| Outlook Windows (moteur Word) | Ignore `max-width`/`width` CSS sur `<img>` → attributs `width`/`height` obligatoires. Pas de `border-radius`, marges de `<div>` peu fiables → `<table>`. Peut afficher l'image aussi comme pièce jointe (trombone). Mise à l'échelle DPI 120 % si pas de `width`. |
| Nouvel Outlook, OWA, Outlook mobile | Images CID affichées ; parfois listées aussi en pièce jointe. |
| Apple Mail (macOS, iOS) | Rendu complet. |
| Thunderbird | Rendu complet. Grise ce qui suit `-- ` en texte brut ; en réponse texte, retire tout ce qui suit `-- `, citation comprise (la signature étant au-dessus de la citation). |
| Mode sombre (Apple Mail, Gmail app, Outlook) | Couleurs inversées : un logo PNG transparent foncé peut devenir invisible → fond opaque clair ou contour. |
| SVG, WebP | Non affichés par Gmail / Outlook : refusés à la configuration. |
| Écrans Retina | Fournir l'image en 2× et fixer `width`/`height` à la taille 1×. |
| Listes de diffusion | Certaines réécrivent ou aplatissent `multipart/related` : images perdues, texte conservé. |

Poids : les images voyagent dans chaque message ; viser < 50 Ko au total.
