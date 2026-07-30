# secondbrain — Technische Spezifikation

**Version:** 1.0 · **Stand:** implementiert

Dieses Dokument beschreibt das Verhalten von secondbrain so genau, dass man es
nachbauen oder prüfen kann. Wo es und die README auseinandergehen, gilt dieses
Dokument.

---

## Inhalt

1. [Geltungsbereich und Begriffe](#1-geltungsbereich-und-begriffe)
2. [Laufzeit und Prozessmodell](#2-laufzeit-und-prozessmodell)
3. [Konfiguration](#3-konfiguration)
4. [Neuladen der Konfiguration](#4-neuladen-der-konfiguration)
5. [OAuth-2.1-Autorisierungsserver](#5-oauth-21-autorisierungsserver)
6. [Sitzungszustand](#6-sitzungszustand)
7. [MCP-Transport](#7-mcp-transport)
8. [Datenmodell](#8-datenmodell)
9. [Pfadauflösung](#9-pfadauflösung)
10. [Der Index](#10-der-index)
11. [Der Schreibpfad](#11-der-schreibpfad)
12. [Section-Edit-Semantik](#12-section-edit-semantik)
13. [Werkzeugreferenz](#13-werkzeugreferenz)
14. [Fehler](#14-fehler)
15. [Audit-Log](#15-audit-log)
16. [Metriken](#16-metriken)
17. [CLI](#17-cli)
18. [Betrieb](#18-betrieb)
19. [Quelltextaufbau](#19-quelltextaufbau)
20. [Testanforderungen](#20-testanforderungen)
21. [Grenzen und bekannte Schwächen](#21-grenzen-und-bekannte-schwächen)

---

## 1. Geltungsbereich und Begriffe

| Begriff | Bedeutung |
| --- | --- |
| **User** | Ein Prinzipal aus der Konfiguration. Trägt Passwort, Vault-Erlaubnisliste und `read_only`-Flag. Die Einheit der Autorisierung. |
| **Vault** | Ein Verzeichnis unterhalb von `data_dir` mit gültigem Namen. Enthält Notizen, `.secondbrain/` und optional `.git/`. |
| **Note** | Eine Datei mit der Endung `.md` innerhalb eines Vaults, mit optionalem YAML-Frontmatter. |
| **Attachment** | Jede indizierte Datei im Vault, die keine `.md`-Datei ist. |
| **Client** | Ein per OAuth-DCR registrierter MCP-Client. |
| **Session** | Ein ausgegebener Access Token, gebunden an genau einen User; dazu optional eine `Mcp-Session-Id`. |
| **Tool** | Eine der 34 über MCP angebotenen Operationen. |

Die Schlüsselwörter **MUSS**, **SOLLTE** und **KANN** sind im Sinne von
RFC 2119 zu lesen.

## 2. Laufzeit und Prozessmodell

- **Sprache:** Go 1.25, Modulpfad `github.com/andreaskasper/secondbrain`
- **Direkte Abhängigkeiten:** `github.com/fsnotify/fsnotify`,
  `github.com/go-git/go-git/v5`, `golang.org/x/crypto`, `golang.org/x/term`,
  `gopkg.in/yaml.v3`, `modernc.org/sqlite`
- **Binary:** ein statisches Binary, `CGO_ENABLED=0`. `modernc.org/sqlite` ist
  ein SQLite in reinem Go; deshalb braucht der FTS5-Index keine libc.
- **Image:** mehrstufiger Build, Laufzeitstufe
  `gcr.io/distroless/static-debian12:nonroot`
- **Port:** `2020/tcp`, reines HTTP. TLS wird vorgelagert terminiert.
- **Dateisystem:** schreibend. Anders als bei aegis ist ein
  schreibgeschütztes Root-Dateisystem nicht möglich (§18).
- **Signale:** `SIGHUP` lädt die Konfiguration neu, sofern sie aus einer Datei
  stammt. `SIGINT`/`SIGTERM` fahren geordnet herunter, mit 10 s Abflussfrist.
- **HTTP-Server:** `ReadHeaderTimeout` 10 s, `IdleTimeout` 120 s.
- **Health:** `GET /healthz` liefert unauthentifiziert
  `{"status":"ok","version":…,"vaults":<Anzahl>}`.

### 2.1 Nebenläufigkeit

| Element | Schutz |
| --- | --- |
| Vault-Registrierung (`VaultManager.vaults`) | `sync.RWMutex` |
| Mutationen innerhalb eines Vaults | `Vault.writeMu`, ein Mutex pro Vault. Lesevorgänge sind nicht betroffen. |
| Index | ein `sync.Mutex` pro `Index`, dazu `SetMaxOpenConns(1)` auf der SQLite-Verbindung |
| Git-Repository | `GitStore.mu`, ein Mutex pro Vault |
| Sitzungszustand | ein `sync.Mutex` über allen Tabellen |
| MCP-Sitzungen | eigener `sync.Mutex` |
| Aktive Konfiguration | `atomic.Pointer[Config]`; ein Reload tauscht den Zeiger |

Der Schreib-Mutex gilt pro Vault, nicht pro Datei. Der Preis ist, dass zwei
Schreibvorgänge in einem Vault serialisiert werden; der Gegenwert ist, dass
zwei Tool-Aufrufe niemals innerhalb einer Datei verschränkt werden können.

### 2.2 Hintergrundprozesse

| Goroutine | Intervall | Aufgabe |
| --- | --- | --- |
| Session-Janitor | 60 s | Entfernt abgelaufene Codes, Tokens, CSRF-Einträge und alte Familieneinträge |
| Housekeeping | 1 h | Räumt MCP-Sitzungen, die 24 h ungenutzt sind, und entleert den Papierkorb nach `trash_retention` |
| Config-Watcher | 2 s | Nur wenn die Konfiguration aus einer Datei stammt; prüft mtime, Größe und Inode |
| fsnotify-Watcher | ereignisgesteuert | Einer pro Vault, entprellt mit 400 ms |

## 3. Konfiguration

Die Konfiguration entsteht in dieser Reihenfolge:

1. Voreinstellungen im Code
2. Falls vorhanden: die YAML-Datei unter `SECONDBRAIN_CONFIG`
   (Standard `/etc/secondbrain/config.yaml`). Ein fehlender Pfad ist **kein**
   Fehler.
3. Umgebungsvariablen. Sie überschreiben die Datei für alle Serverwerte.
4. Benutzer: Enthält die Datei eine nichtleere `users:`-Liste, gilt
   ausschließlich diese Liste; `SECONDBRAIN_USERNAME` und
   `SECONDBRAIN_PASSWORD` werden dann vollständig ignoriert.
5. Validierung. Schlägt sie fehl, startet der Prozess nicht.

Punkt 4 ist die einzige Stelle, an der die Datei die Umgebung schlägt. Der
Grund ist, dass jede andere Regel eine Frage der Form „welches Passwort
gewinnt?" erzeugt, und eine solche Frage in einem Authentifizierungspfad ist
ein Fehler, der nur noch nicht geschrieben wurde.

### 3.1 Umgebungsvariablen

| Variable | Typ | Standard | Wirkung |
| --- | --- | --- | --- |
| `SECONDBRAIN_USERNAME` | String | — | Anmeldename. Pflicht, wenn keine `users:`-Liste existiert. |
| `SECONDBRAIN_PASSWORD` | String | — | Passwort. Literal, `bcrypt:<hash>`, `env:NAME` oder `file:/pfad`. Pflicht wie oben. |
| `SECONDBRAIN_PUBLIC_URL` | URL | — | **Pflicht.** Absolute `http`- oder `https`-URL ohne Query und Fragment. Alle OAuth-Endpunkte werden daraus abgeleitet. |
| `SECONDBRAIN_LISTEN` | String | `:2020` | Adresse des HTTP-Listeners. Darf nicht leer sein. |
| `SECONDBRAIN_DATA` | Pfad | `/data` | Vault-Wurzel. Muss absolut sein. Wird beim Start angelegt. |
| `SECONDBRAIN_DEFAULT_VAULT` | String | `default` | Vault, den ein Tool-Aufruf ohne `vault` meint. Muss dem Vault-Namensmuster genügen. |
| `SECONDBRAIN_CONFIG` | Pfad | `/etc/secondbrain/config.yaml` | Optionale Konfigurationsdatei. |
| `SECONDBRAIN_GIT` | Bool | `true` | Versionierung ein- oder ausschalten. Nicht parsbar → Startfehler. |
| `SECONDBRAIN_GIT_REMOTE` | URL | — | Wenn gesetzt, wird nach jedem Commit gepusht. |
| `SECONDBRAIN_GIT_TOKEN` | String | — | Passwort für den Push (HTTP Basic, Benutzername `secondbrain`). |
| `SECONDBRAIN_GIT_AUTHOR` | String | `secondbrain` | Name in Author und Committer. |
| `SECONDBRAIN_GIT_EMAIL` | String | `secondbrain@localhost` | E-Mail in Author und Committer. |
| `SECONDBRAIN_MAX_RESPONSE_BYTES` | Int | `262144` | Obergrenze für ein einzelnes Tool-Ergebnis. Werte unter 4096 sind ein Startfehler. |
| `SECONDBRAIN_TOKEN_TTL` | Duration | `12h` | Gültigkeit eines Access Tokens. Muss positiv sein. |
| `SECONDBRAIN_CODE_TTL` | Duration | `60s` | Gültigkeit eines Authorization Codes. |
| `SECONDBRAIN_TRASH_RETENTION` | Duration | `720h` | Aufbewahrung im Papierkorb. 720 h sind 30 Tage. |
| `SECONDBRAIN_ALLOWED_ORIGINS` | Liste | leer | Kommagetrennte Origins für `/mcp`. Leer bedeutet **keine** Origin-Prüfung. |
| `SECONDBRAIN_LOG_LEVEL` | Enum | `info` | `debug`, `info`, `warn`, `error`. Unbekannte Werte ergeben `info`. |
| `SECONDBRAIN_METRICS` | Bool | `false` | Prometheus-Endpunkt ein- oder ausschalten. Nicht parsbar → Startfehler. Einzelheiten in §16. |
| `SECONDBRAIN_METRICS_PATH` | Pfad | `/metrics` | Pfad des Endpunkts. Muss mit `/` beginnen und darf nicht mit `/mcp`, `/healthz` oder `/` zusammenfallen. |
| `SECONDBRAIN_METRICS_KEY` | String | — | Gemeinsamer Schlüssel für den Endpunkt. Literal, `env:NAME` oder `file:/pfad`. Kürzer als 16 Zeichen → Startfehler. |
| `SECONDBRAIN_METRICS_LISTEN` | String | — | Eigene Adresse für die Metriken, z. B. `:9090`. Gesetzt heißt: ausschließlich dort. |

Durations werden mit `time.ParseDuration` gelesen (`30s`, `12h`, `720h`).

### 3.2 YAML-Felder

Jedes Serverfeld hat denselben Namen wie die zugehörige Umgebungsvariable,
klein geschrieben und ohne Präfix.

| Feld | Typ | Standard | Wirkung |
| --- | --- | --- | --- |
| `listen` | String | `:2020` | wie `SECONDBRAIN_LISTEN` |
| `public_url` | String | — | Pflicht, sofern nicht über die Umgebung gesetzt |
| `allowed_origins` | Liste[String] | `[]` | erlaubte Origins; leer heißt keine Prüfung |
| `data_dir` | String | `/data` | Vault-Wurzel |
| `default_vault` | String | `default` | Standard-Vault |
| `max_response_bytes` | Int | `262144` | Obergrenze pro Tool-Ergebnis, mindestens 4096 |
| `token_ttl` | String | `12h` | Duration |
| `code_ttl` | String | `60s` | Duration |
| `trash_retention` | String | `720h` | Duration |
| `login_rate_limit` | String | `10/m` | `<Anzahl>/<s\|m\|h>`, gilt pro Quell-IP |
| `git` | Bool | `true` | Versionierung |
| `git_remote` | String | `""` | Push-Ziel |
| `git_token` | String | `""` | Push-Credential |
| `git_author` | String | `secondbrain` | Commit-Autor |
| `git_email` | String | `secondbrain@localhost` | Commit-E-Mail |
| `metrics` | Bool | `false` | Prometheus-Endpunkt, siehe §16 |
| `metrics_path` | String | `/metrics` | Pfad des Endpunkts |
| `metrics_key` | String | `""` | Schlüssel für den Endpunkt; `env:` und `file:` werden aufgelöst |
| `metrics_listen` | String | `""` | eigene Adresse für die Metriken, z. B. `:9090` |
| `users` | Liste[User] | — | siehe §3.3 |

`login_rate_limit` ist ausschließlich über die Datei einstellbar; es gibt
dafür keine Umgebungsvariable.

### 3.3 Benutzer

```yaml
users:
  - name: andreas            # Pflicht, eindeutig
    password: "bcrypt:$2a$12$…"   # Pflicht, nicht leer
    vaults: []               # optional, leer = alle Vaults
    read_only: false         # optional, Standard false
```

| Feld | Typ | Standard | Wirkung |
| --- | --- | --- | --- |
| `name` | String | — | Anmeldename. Leer oder doppelt → Startfehler. |
| `password` | String | — | Literal, `bcrypt:`, `env:` oder `file:`. Nach Auflösung leer → Startfehler. Ein Literal muss mindestens 8 Zeichen haben. |
| `vaults` | Liste[String] | `[]` | Leer bedeutet jeder Vault im Datenverzeichnis, auch später angelegte. Jeder Eintrag muss dem Vault-Namensmuster genügen. |
| `read_only` | Bool | `false` | Bei `true` werden die 18 mutierenden Werkzeuge in `tools/list` gar nicht erst aufgeführt, und ein direkter Aufruf wird abgelehnt. |

### 3.4 Auflösung von Geheimnissen

| Präfix | Verhalten |
| --- | --- |
| *(keines)* | Literalwert |
| `env:NAME` | `os.Getenv("NAME")`. Leer oder nicht gesetzt → Konfigurationsfehler. |
| `file:/pfad` | Dateiinhalt, abschließende `\r` und `\n` werden entfernt. Nicht lesbar → Konfigurationsfehler. |
| `bcrypt:$2…` | Nur für Passwörter. Das Präfix wird abgeschnitten, der Rest als bcrypt-Hash gespeichert. |

`env:` und `file:` werden **vor** dem `bcrypt:`-Test ausgewertet, das heißt
`password: "env:X"` mit `X="bcrypt:$2a$…"` ergibt ebenfalls einen Hash.

Diese Auflösung gilt in v1 für Passwörter und für `metrics_key`. `git_token`
wird dagegen als Literal übernommen; eine `file:`-Angabe dort landet
unverändert als Push-Credential (§21).

### 3.5 Passwortprüfung

- Gehashte Passwörter über `bcrypt.CompareHashAndPassword`.
- Literale Passwörter über `subtle.ConstantTimeCompare`.
- Bei unbekanntem Benutzernamen wird trotzdem ein bcrypt-Vergleich gegen einen
  festen Dummy-Hash durchgeführt, damit die Antwortzeit keine gültigen
  Benutzernamen verrät.

### 3.6 Validierung

Der Start scheitert, wenn:

- `public_url` fehlt, nicht absolut ist, weder `http` noch `https` verwendet
  oder Query bzw. Fragment enthält
- `listen` leer ist
- `data_dir` nicht absolut ist
- `default_vault` nicht `^[a-z0-9][a-z0-9_-]{0,63}$` entspricht
- kein Benutzer konfiguriert ist
- ein Benutzername doppelt vorkommt oder leer ist
- ein Literalpasswort kürzer als 8 Zeichen ist
- `max_response_bytes` kleiner als 4096 ist
- eine Duration oder eine Ratenangabe nicht parsbar oder nicht positiv ist
- eine `env:`- oder `file:`-Referenz nicht auflösbar ist
- `metrics` an ist und `metrics_path` nicht mit `/` beginnt oder mit `/mcp`,
  `/healthz` bzw. `/` zusammenfällt
- `metrics` an ist und ein gesetzter `metrics_key` nach der Auflösung kürzer
  als 16 Zeichen ist

### 3.7 Ratensyntax

`<Anzahl>/<Einheit>` mit Einheit `s`, `m` oder `h`. Beispiele: `10/m`,
`600/m`, `20/h`. Die Anzahl muss positiv sein.

## 4. Neuladen der Konfiguration

Ein Watcher läuft nur, wenn die Konfiguration aus einer Datei stammt
(`Source != "environment"`).

- Alle 2 Sekunden werden mtime, Größe und Inode der Datei verglichen. Der
  Inode ist dabei wesentlich: ein atomarer Austausch ändert ihn, ohne dass
  sich Größe oder mtime ändern müssen.
- `SIGHUP` erzwingt sofort einen Reload.
- Ein Reload lädt, löst auf und validiert in ein **neues** `Config`-Objekt.
  Nur bei vollständigem Erfolg wird der aktive Zeiger getauscht. Andernfalls
  bleibt die alte Konfiguration aktiv und es wird eine Zeile
  `config_reload_rejected` auf `error` geloggt.
- `data_dir` und `public_url` werden beim Reload **nicht** übernommen. Vaults
  und ausgegebene Tokens sind an sie gebunden. Eine Änderung wird ignoriert
  und als `config_reload_partial` geloggt.
- Das Login-Ratenlimit wird mit dem neuen Wert neu aufgebaut.
- Laufende Anfragen arbeiten mit der Konfiguration weiter, mit der sie
  begonnen haben.
- **Sitzungen überleben einen Reload.** Ein Token bleibt gültig, solange sein
  User noch existiert; ist der User entfernt worden, schlägt die nächste
  Nutzung fehl. Ein geändertes Passwort widerruft bestehende Tokens nicht.
- Neu angelegte Vault-Verzeichnisse werden nicht durch den Reload erkannt,
  sondern beim ersten Zugriff über `VaultManager.Get`.

## 5. OAuth-2.1-Autorisierungsserver

Alle Endpunkte werden aus `public_url` abgeleitet.

### 5.1 `GET /.well-known/oauth-protected-resource`

Auch unter `/.well-known/oauth-protected-resource/mcp` erreichbar.

```json
{
  "resource": "https://notes.example.com/mcp",
  "authorization_servers": ["https://notes.example.com"],
  "bearer_methods_supported": ["header"],
  "scopes_supported": ["secondbrain"]
}
```

### 5.2 `GET /.well-known/oauth-authorization-server`

Auch unter `/.well-known/oauth-authorization-server/mcp` erreichbar.

```json
{
  "issuer": "https://notes.example.com",
  "authorization_endpoint": "https://notes.example.com/authorize",
  "token_endpoint": "https://notes.example.com/token",
  "registration_endpoint": "https://notes.example.com/register",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"],
  "scopes_supported": ["secondbrain"]
}
```

Beide Dokumente werden unauthentifiziert mit `Cache-Control: max-age=300`
ausgeliefert.

### 5.3 `POST /register` — Dynamic Client Registration (RFC 7591)

- Ratenlimit 20/h pro Quell-IP.
- Rumpf maximal 64 KiB.
- Mindestens eine `redirect_uri` ist Pflicht. Jede MUSS absolut sein, DARF
  kein Fragment enthalten und MUSS `https` verwenden — außer bei
  `localhost`, `127.0.0.1` oder `::1`, wo `http` zulässig ist.
- Nur öffentliche Clients: ein von `none` abweichendes
  `token_endpoint_auth_method` wird abgelehnt. PKCE ist damit zwingend.
- `client_id` sind 16 Zufallsbytes als base64url.
- Die Client-Tabelle ist auf 1000 Einträge begrenzt; bei Überlauf wird der am
  längsten ungenutzte Eintrag verworfen.

Antwort `201` mit `client_id`, `client_id_issued_at`, `client_name`,
`redirect_uris`, `grant_types`, `response_types` und
`token_endpoint_auth_method`.

### 5.4 `GET /authorize`

Geprüft wird, bevor irgendetwas gerendert wird:

1. `client_id` bekannt — sonst HTML-Fehlerseite mit `400`, **keine**
   Weiterleitung.
2. `redirect_uri` stimmt exakt mit einer registrierten überein (konstante
   Zeit) — sonst HTML-Fehlerseite mit `400`. Eine Weiterleitung auf eine
   ungeprüfte URI wäre ein offener Redirector.
3. `response_type=code` — sonst Weiterleitung mit
   `error=unsupported_response_type`.
4. `code_challenge_method=S256` und nichtleere `code_challenge` — sonst
   Weiterleitung mit `error=invalid_request`.

Danach wird eine in sich geschlossene HTML-Anmeldeseite ausgeliefert:
Benutzername, Passwort, Absenden. Sie trägt ein einmal verwendbares
CSRF-Token, das an `client_id`, `redirect_uri`, `state` und `code_challenge`
gebunden ist und 10 Minuten gilt.

Header der Seite: `Cache-Control: no-store`, `X-Frame-Options: DENY`,
`Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff` und eine
CSP mit `default-src 'none'`, Nonce für Style und Script,
`frame-ancestors 'none'`, `base-uri 'none'` sowie
`form-action 'self' <Origin der redirect_uri>`.

Der Zusatz bei `form-action` ist notwendig und nicht offensichtlich: Browser
wenden `form-action` auch auf die Weiterleitung an, die auf das Absenden
folgt. Mit bloßem `'self'` gelingt der POST, secondbrain antwortet mit 302 auf
den Callback des Clients — und der Browser folgt ihm stillschweigend nicht.
Der Benutzer klickt auf „Sign in" und es passiert nichts.

### 5.5 `POST /authorize`

1. Client und `redirect_uri` erneut prüfen.
2. CSRF-Token einlösen (einmalig). Ist es unbekannt oder abgelaufen, wird eine
   Fehlerseite ausgeliefert, die erklärt, dass das Formular verbraucht ist und
   der Vorgang im Client neu gestartet werden muss. Ein neues Formular wäre
   nutzlos, weil die PKCE-Challenge nicht nacherfunden werden kann.
3. Ratenlimit pro Quell-IP (`login_rate_limit`, Standard `10/m`). Überschritten
   → `429` mit erneut gerendertem Formular.
4. Passwort prüfen (§3.5). Fehlschlag → `401` mit der generischen Meldung
   „Invalid username or password."; unbekannter Benutzer und falsches Passwort
   sind nicht unterscheidbar. Es wird `login_failed` geloggt.
5. Erfolg → Authorization Code erzeugen und mit `302` auf
   `redirect_uri?code=…&state=…` weiterleiten.

Ein Authorization Code besteht aus 32 Zufallsbytes (base64url), ist einmal
verwendbar, lebt `code_ttl` lang und ist an User, `client_id`, `redirect_uri`
und `code_challenge` gebunden. Es gibt keinen Zustimmungsdialog.

### 5.6 `POST /token`

`application/x-www-form-urlencoded`.

**`grant_type=authorization_code`** verlangt `code`, `client_id` und
`code_verifier`. `redirect_uri` ist bewusst **nicht** erforderlich: der Code
ist bereits an die bei `/authorize` gegen die Registrierung geprüfte
`redirect_uri` gebunden, und viele aktuelle Clients senden das Feld hier nicht
mehr. Wird es gesendet, muss es passen.

Geprüft wird: der Code existiert, ist nicht abgelaufen und nicht verbraucht;
`client_id` stimmt (konstante Zeit); `redirect_uri` stimmt, falls gesendet;
`BASE64URL(SHA256(code_verifier))` entspricht der `code_challenge`, wobei der
Verifier zwischen 43 und 128 Zeichen lang sein muss; der User existiert noch.
Der Code wird beim Einlösen entfernt, unabhängig vom Ergebnis.

**`grant_type=refresh_token`** verlangt `refresh_token` und `client_id`. Der
Refresh Token wird rotiert: der alte wird sofort ungültig. Ein bereits
rotierter Token, der erneut vorgelegt wird, tötet die gesamte Token-Familie
für diesen Client und User und wird als `token_reuse_detected` geloggt. Eine
abweichende `client_id` wird ebenso behandelt.

Antwort `200`:

```json
{
  "access_token": "<32 Zufallsbytes, base64url>",
  "token_type": "Bearer",
  "expires_in": 43200,
  "refresh_token": "<32 Zufallsbytes, base64url>",
  "scope": "secondbrain"
}
```

Fehler folgen RFC 6749 §5.2 mit HTTP `400` und `Cache-Control: no-store`.
Jeder Fehlschlag erzeugt eine `token_failed`-Logzeile mit einem
maschinenlesbaren Grund — ein stiller Token-Endpunkt ist unmöglich zu
debuggen, weil der Fehler zwischen Browser und Client-Backend passiert.

### 5.7 Bearer-Authentifizierung auf `/mcp`

`Authorization: Bearer <access_token>`. Fehlt der Header oder ist der Token
unbekannt oder abgelaufen:

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer resource_metadata="https://notes.example.com/.well-known/oauth-protected-resource"
```

Tokens werden über ihren SHA-256-Hash nachgeschlagen. Existiert der User in
der aktuellen Konfiguration nicht mehr, gilt der Token als ungültig.

### 5.8 Ratenlimits

| Grenze | Schlüssel | Standard |
| --- | --- | --- |
| Fehlgeschlagene Anmeldungen | Quell-IP | `10/m`, über `login_rate_limit` änderbar |
| Client-Registrierungen | Quell-IP | `20/h`, fest |
| Tool-Aufrufe | Benutzername | `600/m`, fest |
| Metrik-Scrapes | Quell-IP | `60/m`, fest (§16.5) |

Alle vier sind Token-Buckets, die kontinuierlich über das Fenster nachgefüllt
werden. Buckets, die länger als 10 Minuten unbenutzt sind, werden bei Bedarf
verworfen; die Zahl der Schlüssel ist auf 10 000 begrenzt.

## 6. Sitzungszustand

Alles Folgende liegt ausschließlich im Arbeitsspeicher und geht bei einem
Neustart verloren.

| Tabelle | Schlüssel | Inhalt | Lebensdauer |
| --- | --- | --- | --- |
| `clients` | `client_id` | Name, Redirect-URIs, Erstellung | keine, max. 1000 (LRU) |
| `codes` | SHA-256 des Codes | User, Client, Redirect-URI, Challenge | `code_ttl` |
| `tokens` | SHA-256 des Tokens | User, Client, Familie | `token_ttl` |
| `refresh` | SHA-256 des Tokens | User, Client, Familie | 30 Tage |
| `csrf` | SHA-256 des Tokens | Client, Redirect-URI, State, Challenge | 10 Minuten |
| `consumedRefresh` | SHA-256 des Tokens | Familie, Zeitpunkt | 30 Tage |
| `deadFamilies` | Familien-ID | Zeitpunkt der Tötung | 30 Tage |
| MCP-Sitzungen | `Mcp-Session-Id` | Hash des zugehörigen Access Tokens | 24 h ohne Nutzung |

Alle geheimnistragenden Werte werden als SHA-256-Hash gespeichert; der
Klartext existiert nur in der Antwort, die ihn ausgegeben hat. Die
Token-Tabelle ist auf 10 000 Einträge begrenzt; bei Überlauf wird der am
frühesten ablaufende Eintrag verworfen und `token_evicted` geloggt.

`consumedRefresh` ist der Grund, warum ein Wiedereinlösen überhaupt erkannt
werden kann: ohne diese Tabelle sähe ein gestohlener, bereits rotierter Token
wie ein unbekannter aus.

## 7. MCP-Transport

- **Endpunkt:** `/mcp`
- **Protokoll:** MCP über Streamable HTTP, JSON-RPC 2.0, Protokollversion
  `2025-06-18`
- **`POST /mcp`** — eine einzelne JSON-RPC-Anfrage oder ein Array (Batch).
  Antwort immer `application/json` mit `Cache-Control: no-store`. Rumpf
  maximal 8 MiB.
- **`GET /mcp`** — öffnet einen SSE-Strom. In v1 werden ausschließlich
  Keep-alive-Kommentare alle 30 Sekunden gesendet.
- **`DELETE /mcp`** — beendet die Sitzung aus `Mcp-Session-Id` und antwortet
  `204`. Die Sitzung wird nur beendet, wenn der vorgelegte Token derjenige
  ist, der sie erzeugt hat; ohne diese Prüfung könnte jeder authentifizierte
  Aufrufer fremde Sitzungen beenden, indem er sie benennt.
- **`Origin`** — wird nur geprüft, wenn `allowed_origins` nichtleer ist. Dann
  sind der eigene Issuer, jeder aufgeführte Eintrag und `*` erlaubt, alles
  andere ergibt `403`. Leer bedeutet keine Prüfung: `/mcp` ist Bearer-
  geschützt und auf einem öffentlichen Host erreichbar, und eine harte
  Origin-Prüfung bricht gehostete Clients, ohne etwas zu gewinnen.

### 7.1 Sitzungen

`Mcp-Session-Id` wird bei `initialize` ausgegeben und ist an den Hash des
Access Tokens gebunden. Sendet ein Client bei einer anderen Methode eine
Sitzungs-ID, die unbekannt ist oder zu einem anderen Token gehört, antwortet
der Server mit dem JSON-RPC-Fehler `-32600` („unknown or mismatched session").

Bei `initialize` wird eine bereits gehaltene, gültige Sitzungs-ID
wiederverwendet, statt bei jedem Reconnect eine neue auszugeben — sonst
hinterlässt ein instabiler Client eine Spur lebender Sitzungen.

### 7.2 Methoden

| Methode | Verhalten |
| --- | --- |
| `initialize` | Liefert `protocolVersion`, `serverInfo` (`secondbrain`, Version), `capabilities: {tools:{listChanged:true}}` und `instructions` (§7.3) |
| `notifications/initialized` | Wird angenommen, keine Antwort |
| `notifications/cancelled` | Wird angenommen, keine Antwort |
| `ping` | Leeres Ergebnis; als Notification ohne Antwort |
| `tools/list` | `{"tools":[…]}` nach §13, gefiltert nach `read_only` |
| `tools/call` | §13 |
| unbekannt | `-32601`, sofern eine `id` vorhanden ist; sonst keine Antwort |

### 7.3 Die `instructions` der `initialize`-Antwort

Zusammengesetzt aus drei Teilen:

1. Generische Grundregeln: erst suchen, dann schreiben; vor dem Ändern lesen
   und den `content_hash` zurückgeben; `note_edit` und `note_section_edit`
   gegenüber `note_write` bevorzugen; `dry_run` bei allem, was mehr als eine
   Datei berührt; `note_outline` vor `note_read` bei langen Notizen.
2. Der Name des Standard-Vaults und die Liste der für diesen User sichtbaren
   Vaults. Bei einem `read_only`-User zusätzlich der Hinweis, dass keine
   verändernden Werkzeuge angeboten werden.
3. Für jeden sichtbaren Vault der Inhalt von
   `<vault>/.secondbrain/instructions.md`, sofern nichtleer, unter einer
   Überschrift mit dem Vault-Namen.

Damit reisen die Konventionen einer Wissensbasis mit der Wissensbasis.

### 7.4 Batch-Verhalten

Beginnt der Rumpf mit `[`, wird er als Array von Anfragen gelesen und in
Reihenfolge abgearbeitet. Antworten werden gesammelt und als Array
zurückgegeben. Enthält der Batch ausschließlich Notifications, ist die
Antwort `202` ohne Rumpf. Erzeugt eine `initialize`-Anfrage im Batch eine
Sitzungs-ID, wird die erste solche im `Mcp-Session-Id`-Header der
Gesamtantwort ausgegeben.

### 7.5 Fehlerkonvention

Ein **Protokollfehler** ist ein JSON-RPC-Fehlerobjekt: `-32700` bei
unlesbarem JSON, `-32600` bei ungültiger Anfrage oder falscher Sitzung,
`-32601` bei unbekannter Methode oder unbekanntem Tool, `-32602` bei
unlesbaren `tools/call`-Parametern.

Ein **Werkzeugfehler** ist dagegen ein *erfolgreiches* JSON-RPC-Ergebnis mit
`isError: true` und dem Fehlertext als Textblock:

```json
{"jsonrpc":"2.0","id":7,"result":{
  "isError": true,
  "content": [{"type":"text","text":"the note changed since it was read: wiki/x.md has content_hash 3f… — read it again before writing"}]}}
```

Das ist so gewollt: Das Modell soll die Meldung sehen und sich korrigieren
können. Ein JSON-RPC-Fehler wird von vielen Clients gar nicht an das Modell
weitergereicht.

### 7.6 Ergebnisform

Ein erfolgreicher `tools/call` liefert:

```json
{"content": [{"type":"text","text":"<JSON, eingerückt>"}],
 "structuredContent": { … dasselbe Objekt … }}
```

Überschreitet das eingerückte JSON `max_response_bytes`, wird es abgeschnitten
und mit einem Hinweis versehen, der die tatsächliche und die zulässige Größe
nennt und auf `limit`/`offset` verweist. `structuredContent` wird nur gesetzt,
wenn das Ergebnis ein Objekt ist.

## 8. Datenmodell

### 8.1 Verzeichnisaufbau

```
<data_dir>/
└── <vault>/
    ├── <beliebige Verzeichnisse>/…    Notizen und Anhänge
    ├── .gitignore                     beim Anlegen des Repositories geschrieben
    ├── .git/                          ein Repository je Vault, wenn git aktiv ist
    └── .secondbrain/
        ├── index.db                   SQLite mit FTS5 (dazu -wal und -shm)
        ├── trash/                     zeitgestempelte Kopien
        └── instructions.md            Konventionen des Vaults
```

Ein Vault-Name MUSS `^[a-z0-9][a-z0-9_-]{0,63}$` entsprechen. Dieses Muster ist
der Grund, warum bei Vault-Namen keine Traversal-Prüfung nötig ist: ein Name,
der darauf passt, enthält weder Trenner noch Punkt noch Nullbyte, und
`filepath.Join(data_dir, name)` kann `data_dir` nicht verlassen.

Beim Start werden alle Unterverzeichnisse von `data_dir` gelesen, die dem
Muster entsprechen, und geöffnet. Existiert keines, wird `default_vault` mit
dem Layout `wiki-raw` angelegt, damit der erste Tool-Aufruf ein Ziel hat.

### 8.2 `.secondbrain/`

| Eintrag | Inhalt | Wegwerfbar |
| --- | --- | --- |
| `index.db` | SQLite-Datenbank mit FTS5-Index, WAL aktiviert | ja, wird neu aufgebaut |
| `trash/` | Kopien überschriebener und gelöschter Notizen, Dateiname `YYYYMMDD-HHMMSS-<pfad mit / ersetzt durch __>` | nein, das ist das Sicherungsnetz |
| `instructions.md` | Konventionstext, geht in die `initialize`-Antwort | nein, aber jederzeit von Hand ersetzbar |

Das gesamte Verzeichnis ist über kein Werkzeug erreichbar (§9) und wird von
`.gitignore` aus der Versionierung ausgeschlossen. Ein SQLite-Index in jedem
Commit würde das Repository aufblähen und jeden Diff unbrauchbar machen.

### 8.3 Notizformat

Eine Notiz ist eine `.md`-Datei. Ein Frontmatter-Block zählt nur, wenn die
Datei mit `---\n` oder `---\r\n` beginnt; ein `---` weiter unten ist eine
horizontale Linie. Der Block endet an der ersten Zeile, die genau `---` oder
`...` ist. Fehlt der Abschluss, gilt die gesamte Datei als Rumpf — geraten
wird nicht. Ein führendes BOM wird toleriert.

Der Frontmatter wird als `yaml.Node` gehalten, nicht als Map. Dadurch bleiben
Schlüsselreihenfolge und Kommentare über ein Lesen und Schreiben hinweg
erhalten: eine von Hand geschriebene Datei soll so wieder herauskommen, wie
sie hineinging.

Erkannt werden:

| Element | Muster |
| --- | --- |
| Überschriften | ATX, `^(#{1,6})\s+(.+?)\s*#*\s*$`, nicht innerhalb von Codeblöcken |
| Wiki-Links | `[[ziel]]`, `[[ziel#anker]]`, `[[ziel\|alias]]`, `[[ziel#anker\|alias]]` |
| Markdown-Links | `[text](ziel)`, optional mit Titel. Externe URLs, `#…`, `mailto:` und `tel:` werden übersprungen |
| Inline-Tags | `#tag`, beginnend mit einem Buchstaben, danach `\w`, `/` und `-`, bis 64 Zeichen |
| Aufgaben | `^(\s*)[-*+]\s+\[( \|x\|X)\]\s*(.*)$` |
| Codeblöcke | ```` ``` ```` oder `~~~` mit bis zu drei führenden Leerzeichen |

Links, Tags und Aufgaben innerhalb eines Codeblocks werden ignoriert. Ein
Shell-Prompt, der mit `#` beginnt, ist keine Überschrift, und ein `#include`
in einem Codebeispiel ist kein Tag.

Zeilennummern für Aufgaben werden um die Länge des Frontmatter-Blocks
verschoben, damit sie mit dem übereinstimmen, was ein Editor anzeigt.

### 8.4 Frontmatter-Felder

| Feld | Gepflegt vom Server | Bedeutung |
| --- | --- | --- |
| `title` | beim Anlegen | Titel. Fehlt er, gilt die erste `#`-Überschrift, sonst der Dateiname ohne Endung. |
| `tags` | bei `note_create`, `note_frontmatter`, `tag_rename`, `note_split` | Liste oder kommagetrennter Skalar. Wird mit Inline-Tags vereinigt. |
| `tag` | nein | wird beim Lesen wie `tags` behandelt |
| `aliases` | bei `note_frontmatter`, `note_merge` | Zusätzliche Namen, unter denen `[[Links]]` auflösen |
| `created` | ja, siehe unten | RFC-3339-Zeitstempel |
| `updated` | ja, siehe unten | RFC-3339-Zeitstempel |
| alle übrigen | nein | frei; `note_frontmatter` setzt und entfernt sie |

`created` und `updated` werden nur berührt, wenn die Notiz bereits einen
Frontmatter-Block hat oder gerade angelegt wird. Eine handgeschriebene Notiz
ohne Metadaten bekommt keine, nur weil ein Werkzeug über sie gelaufen ist.
`created` wird gesetzt, wenn es fehlt; `updated` wird bei jedem Schreibvorgang
gesetzt, außer wenn die Operation `skipTouch` verwendet (§11.5).

Tags werden normalisiert: führendes `#` entfernt, Schrägstriche an den Rändern
entfernt, kleingeschrieben.

### 8.5 Der Inhalts-Hash

`content_hash` ist die hexadezimale Darstellung der ersten 16 Bytes des
SHA-256 über den **gesamten Dateiinhalt** einschließlich Frontmatter, also 32
Zeichen. Er wird beim Lesen zurückgegeben und beim Schreiben verglichen. Der
Vergleich ist unabhängig von Groß- und Kleinschreibung.

### 8.6 Anhänge

Jede indizierte Datei ohne `.md`-Endung gilt als Anhang. Über
`attachment_put` schreibbar sind ausschließlich:

`.png .jpg .jpeg .gif .webp .svg .pdf .txt .csv .json .yaml .yml .mp3 .m4a .wav`

Die Obergrenze liegt bei 32 MiB je Datei nach dem Base64-Dekodieren.

### 8.7 Größenbegrenzung für Notizen

Dateien über 4 MiB werden weder gelesen noch indiziert. `note_read` meldet die
Größe und die Grenze; der Indexlauf überspringt sie mit einer Warnung.

## 9. Pfadauflösung

Jeder von außen kommende Pfad durchläuft `Vault.Resolve`:

1. Leerzeichen an den Rändern entfernen. Leerer Pfad → Fehler.
2. Enthält der Pfad ein Nullbyte → Fehler.
3. `\` wird zu `/`, ein führendes `./` wird entfernt.
4. Ist der Pfad absolut → Fehler.
5. `path.Clean`. Ergibt das `.`, `..` oder etwas mit `../` am Anfang → Fehler.
6. Jedes Segment prüfen: leer → Fehler; beginnt mit `.` → Fehler.
7. `filepath.Join(vault.Root, …)`.
8. Lässt sich der Zielpfad über `filepath.EvalSymlinks` auflösen, MUSS das
   Ergebnis unterhalb der ebenso aufgelösten Vault-Wurzel liegen, sonst
   Fehler.

Schritt 6 ist der eigentliche Schutz und zugleich der Grund, warum es keine
Sperrliste gibt. Es gibt kein Muster zu überlisten, sondern nur eine
Invariante: **kein Pfadbestandteil beginnt mit einem Punkt.** Damit sind in
einem Zug unerreichbar: `.secondbrain/` mit Index und Papierkorb, `.git/` mit
der Historie, `.obsidian/` mit den Einstellungen des Editors und jede andere
versteckte Datei, die dort jemals liegen wird — ohne dass für jede ein
Sonderfall gepflegt werden muss.

Schritt 8 fängt den Fall ab, der von den vorherigen Prüfungen nicht abgedeckt
ist: ein Verzeichnis *innerhalb* des Vaults, das ein Symlink nach außen ist.
Der Pfad ist dann formal einwandfrei und zeigt trotzdem woandershin.

`ResolveNote` ergänzt zusätzlich eine fehlende `.md`-Endung, damit
`wiki/thema` und `wiki/thema.md` dieselbe Notiz bezeichnen. Der Vergleich der
Endung ist unabhängig von der Groß-/Kleinschreibung.

`Vault.Walk` besucht jede sichtbare Datei. Verzeichnisse, deren Name mit einem
Punkt beginnt, werden vollständig übersprungen — dieselbe Regel wie oben, und
damit derselbe Effekt für Index, `note_list`, `vault_grep` und
`attachment_list`.

## 10. Der Index

SQLite über `modernc.org/sqlite`, geöffnet mit
`busy_timeout(5000)`, `journal_mode(WAL)`, `synchronous(NORMAL)` und
`SetMaxOpenConns(1)`.

Der Index ist ein Cache. Schlägt die Migration fehl, wird die Datei gelöscht
und neu angelegt; ein beschädigter Index ist kein Grund, den Dienst zu
verweigern, weil die Notizen davon unberührt sind.

### 10.1 Schema

```sql
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);

CREATE TABLE IF NOT EXISTS notes (
    path     TEXT PRIMARY KEY,
    title    TEXT NOT NULL DEFAULT '',
    mtime    INTEGER NOT NULL DEFAULT 0,
    size     INTEGER NOT NULL DEFAULT 0,
    hash     TEXT NOT NULL DEFAULT '',
    created  TEXT NOT NULL DEFAULT '',
    updated  TEXT NOT NULL DEFAULT '',
    is_note  INTEGER NOT NULL DEFAULT 1,
    words    INTEGER NOT NULL DEFAULT 0
);

CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
    path UNINDEXED, title, body,
    tokenize = "unicode61 remove_diacritics 2"
);

CREATE TABLE IF NOT EXISTS tags (path TEXT NOT NULL, tag TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS tags_tag  ON tags(tag);
CREATE INDEX IF NOT EXISTS tags_path ON tags(path);

CREATE TABLE IF NOT EXISTS links (
    src      TEXT NOT NULL,
    target   TEXT NOT NULL,
    anchor   TEXT NOT NULL DEFAULT '',
    alias    TEXT NOT NULL DEFAULT '',
    wiki     INTEGER NOT NULL DEFAULT 1,
    resolved TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS links_src      ON links(src);
CREATE INDEX IF NOT EXISTS links_resolved ON links(resolved);

CREATE TABLE IF NOT EXISTS headings (
    path TEXT NOT NULL, level INTEGER NOT NULL,
    text TEXT NOT NULL, line INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS headings_path ON headings(path);

CREATE TABLE IF NOT EXISTS aliases (path TEXT NOT NULL, alias TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS aliases_alias ON aliases(alias);

CREATE TABLE IF NOT EXISTS tasks (
    path TEXT NOT NULL, line INTEGER NOT NULL,
    text TEXT NOT NULL, done INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_path ON tasks(path);

CREATE TABLE IF NOT EXISTS embeddings (
    path    TEXT NOT NULL,
    chunk   INTEGER NOT NULL,
    model   TEXT NOT NULL,
    heading TEXT NOT NULL DEFAULT '',
    vector  BLOB NOT NULL,
    PRIMARY KEY (path, chunk, model)
);
```

Anschließend wird in `meta` der Schlüssel `schema` auf `1` gesetzt.

Zu `notes`: die Tabelle enthält **auch** Anhänge, dann mit `is_note = 0`,
`title` gleich dem Dateinamen und leerem `hash`. `mtime` ist
`ModTime().UnixNano()`. `words` ist die Anzahl der durch
`strings.Fields` getrennten Wörter im Rumpf, ohne Frontmatter.

Zu `embeddings`: die Tabelle ist in v1 vollständig leer und wird von keiner
Abfrage gelesen. Sie steht bereits im Schema, damit eine spätere semantische
Suche eine Datenmigration ist und keine Schemamigration, und damit heute
angelegte Indizes dann weiterhin lesbar sind. Beim Entfernen eines Pfades
werden ihre Zeilen mitgelöscht.

### 10.2 Reconcile

`Reconcile` gleicht den Index gegen das Dateisystem ab und läuft beim Start
jedes Vaults, nach `vault_create`, bei `secondbrain reindex` und immer dann,
wenn der Watcher einen großen Schwung Änderungen sieht.

1. `path → (mtime, size)` aus `notes` in eine Map lesen.
2. Über `Vault.Walk` jede sichtbare Datei besuchen und als gesehen markieren.
3. Stimmen `mtime` (auf Nanosekunden) und `size` überein, wird die Datei
   übersprungen. Ein Neustart über einem unveränderten Vault kostet deshalb
   praktisch nichts.
4. Andernfalls wird die Datei neu eingelesen (`ingest`).
5. Jeder bekannte Pfad, der nicht gesehen wurde, wird entfernt.
6. Gab es Änderungen oder Entfernungen, werden die Links neu aufgelöst
   (§10.4).
7. Eine Logzeile `index_reconciled` mit Dateizahl, Änderungen und
   Entfernungen.

`ingest` läuft in einer Transaktion und schreibt der Reihe nach: `notes`,
`notes_fts`, `tags`, `links`, `headings`, `aliases`, `tasks`. Vorher wird der
Pfad aus allen Tabellen entfernt, sodass ein erneutes Einlesen idempotent ist.

`UpdatePath` ist die Einzeldatei-Variante: existiert die Datei nicht mehr,
wird sie entfernt; sonst wird sie neu eingelesen. In beiden Fällen folgt eine
Linkauflösung.

### 10.3 Der Watcher

Ein fsnotify-Watcher je Vault, registriert auf allen sichtbaren
Verzeichnissen. Versteckte Verzeichnisse werden ausgelassen — `.git` ändert
sich bei jedem eigenen Commit und würde einen Ereignissturm erzeugen.

- Ereignisse zu Dateien, deren Name mit `.` oder mit `.sbtmp-` beginnt, werden
  verworfen. Letzteres sind die eigenen Temporärdateien des atomaren
  Schreibens.
- Ereignisse werden gesammelt und **400 ms** nach dem ersten Ereignis
  gemeinsam verarbeitet. Ein einzelnes Speichern im Editor erzeugt mehrere
  inotify-Ereignisse, ein `git checkout` hunderte.
- Wird ein neues Verzeichnis angelegt, wird es mitregistriert und ein
  vollständiger Reconcile vorgemerkt: ein Klon oder ein entpacktes Archiv
  taucht als Verzeichnis auf, das bereits voll ist, und die Ereignisse für
  seinen Inhalt hat man nie gesehen.
- Enthält ein Schwung mehr als 50 Pfade oder die Reconcile-Marke, wird
  vollständig abgeglichen statt einzeln aktualisiert.

Damit sind Obsidian, ein `git pull` und ein `rsync` in dasselbe Verzeichnis
der Normalfall und keine Gefahrenquelle.

### 10.4 Linkauflösung

Nach jeder Änderung wird für jede Zeile in `links` die Spalte `resolved`
gesetzt. Die Reihenfolge entspricht dem, was Obsidian tut und was ein Mensch
erwartet:

1. Beginnt das Ziel mit `./` oder `../`, wird es zunächst gegen das
   Verzeichnis der verlinkenden Notiz aufgelöst (`path.Clean(path.Join(…))`).
2. Ein führendes `/` wird entfernt.
3. **Exakter Pfad**: gibt es eine Notiz genau unter diesem Pfad?
4. **Pfad ohne Endung**: gibt es eine Notiz unter `ziel + ".md"`?
5. **Dateiname**: gibt es genau *eine* Notiz, deren Basisname ohne Endung
   (kleingeschrieben) dem Ziel entspricht?
6. **Titel**: gibt es genau *eine* Notiz mit diesem Titel (kleingeschrieben)?
7. **Alias**: gibt es genau *eine* Notiz mit diesem Alias (kleingeschrieben)?

Die Schritte 5 bis 7 verlangen Eindeutigkeit. Zwei Notizen mit demselben
Basisnamen lösen einen bloßen `[[Namen]]`-Link nicht auf; er gilt dann als
gebrochen, statt willkürlich auf eine der beiden zu zeigen.

Ein Link, für den keine Regel greift, behält `resolved = ''` und erscheint als
gebrochener Link in `note_backlinks`, `vault_stats` und `vault_review`.

### 10.5 Suche und BM25

`note_search` baut eine Abfrage über `notes` mit `is_note = 1`:

- Ist `query` nichtleer, kommt ein Join auf `notes_fts` und
  `notes_fts MATCH ?` hinzu. Ranking über
  `bm25(notes_fts, 8.0, 1.0)` — die Spalte `title` wiegt achtmal so schwer wie
  `body` — und ein Ausschnitt über
  `snippet(notes_fts, 2, '<<', '>>', ' … ', 24)`.
- `prefix` wird zu `path LIKE '<prefix>/%'`.
- `glob` wird zu `path GLOB ?`.
- `modified_after` wird zu `mtime >= ?`.
- Jeder Eintrag in `tags` wird zu einer eigenen `EXISTS`-Bedingung, das heißt
  mehrere Tags werden **und**-verknüpft.

Ohne Suchtext wird nach `mtime DESC` sortiert, mit Suchtext nach dem
BM25-Wert aufsteigend. SQLite liefert für BM25 eine negative Zahl, bei der
kleiner besser ist; das ausgegebene Feld `score` ist das Negative davon, weil
jeder Aufrufer bei einem Feld namens `score` annimmt, dass größer besser ist.

Die Übersetzung der Nutzereingabe in einen FTS5-Ausdruck: enthält die Eingabe
eines der Zeichen `"`, `*`, `(`, `)`, `:` oder die Folgen ` OR ` bzw. ` NOT `,
wird sie unverändert durchgereicht — wer die Syntax kennt, darf sie benutzen.
Andernfalls wird jedes Wort in Anführungszeichen gesetzt und die Wörter werden
mit `AND` verbunden. Ein FTS5-Syntaxfehler wird in eine verständliche Meldung
übersetzt.

### 10.6 Verwandte Notizen

`note_related` ist die bewusst billige Ersatzhandlung für semantische Suche:
kein Modell, kein API-Aufruf, kein Wartungsaufwand. Vier Beiträge werden
addiert:

| Beitrag | Gewicht | Berechnung |
| --- | --- | --- |
| Gemeinsame Tags | 6,0 | Summe über gemeinsame Tags von `1 / (Anzahl Notizen mit diesem Tag)` |
| Ausgehende Links | 3,0 | Anzahl der aufgelösten Links von dieser Notiz zur Kandidatin |
| Eingehende Links | 3,0 | Anzahl der aufgelösten Links von der Kandidatin hierher |
| Ko-Zitation | 1,5 | Anzahl der Notizen, die auf dasselbe zeigen wie diese |

Die Gewichtung der Tags über ihre Seltenheit ist der Kern: alles ist mit
`#notiz` versehen, also sagt `#notiz` nichts. Sortiert wird absteigend nach
Punktzahl, bei Gleichstand nach Pfad.

## 11. Der Schreibpfad

Jede Mutation an einer Notiz läuft durch `Vault.Apply` mit einer `writeOp`.
Ein einziger Pfad bedeutet, dass optimistisches Sperren, Dry-Run, Papierkorb
und Commit Eigenschaften des Systems sind und nicht Dinge, an die jedes
Werkzeug einzeln denken muss.

```go
type writeOp struct {
    rel       string   // Zielpfad, relativ zum Vault
    expected  string   // erwarteter content_hash, "" = keine Prüfung
    dryRun    bool
    reason    string   // Commit-Nachricht; "" = kein Commit
    transform func(cur string, exists bool) (string, error)
    skipTouch bool     // created/updated nicht anfassen
}
```

### 11.1 Ablauf

1. Pfad auflösen (§9).
2. `writeMu` des Vaults sperren.
3. Aktuellen Inhalt lesen. Existiert die Datei nicht, ist `cur` leer und
   `exists` false.
4. **Optimistisches Sperren:** Ist `expected` gesetzt und die Datei existiert,
   MUSS `expected` dem aktuellen Hash entsprechen; sonst Fehler mit dem
   tatsächlichen Hash und der Aufforderung, neu zu lesen. Ist `expected`
   gesetzt und die Datei existiert nicht, ist das ein „nicht gefunden".
5. `transform` aufrufen.
6. Sofern nicht `skipTouch` und das Ergebnis nichtleer ist:
   `created`/`updated` pflegen (§8.4).
7. Ergebnisobjekt bauen: Pfad, Vault, `created`-Flag, `dry_run`-Flag,
   `hash_before`, `content_hash`, `bytes` und den **Unified Diff**.
8. Ist der neue Inhalt identisch mit dem alten, wird `"no change"` gemeldet
   und nichts geschrieben.
9. Bei `dry_run` wird `"dry run: nothing was written"` gemeldet und nichts
   geschrieben. Der Diff steht trotzdem im Ergebnis.
10. Verzeichnisse anlegen.
11. Existierte die Datei, wird ihr bisheriger Inhalt in den Papierkorb kopiert
    (§11.3).
12. Atomar schreiben (§11.4).
13. Index für diesen Pfad aktualisieren; schlägt das fehl, wird gewarnt, nicht
    abgebrochen.
14. Ist `reason` gesetzt und Git aktiv, wird committet; schlägt das fehl, wird
    gewarnt.

### 11.2 Löschen

`Vault.Delete` prüft ebenfalls `expected`, legt eine Kopie in den Papierkorb
und entfernt danach die Datei von ihrem Pfad. Der Commit trägt
`note_delete: <pfad>`. Bei `dry_run` passiert nichts.

Es gibt keinen Weg, über ein Werkzeug eine Notiz zu entfernen, ohne dass
vorher eine Kopie im Papierkorb liegt.

### 11.3 Papierkorb

Zielname:
`<vault>/.secondbrain/trash/YYYYMMDD-HHMMSS-<pfad, / durch __ ersetzt>`,
Zeitstempel in UTC. Der Papierkorb wird stündlich durchgesehen; Dateien, deren
Änderungszeit älter als `trash_retention` ist, werden entfernt und die Anzahl
als `trash_purged` geloggt.

Zwei Kopien innerhalb derselben Sekunde für denselben Pfad überschreiben
einander. Das ist der bekannte scharfe Rand dieses Schemas (§21).

### 11.4 Atomares Schreiben

Eine Temporärdatei `.sbtmp-*` **im selben Verzeichnis**, schreiben, `Sync`,
schließen, `chmod 0644`, `rename`. Ein Absturz mitten im Schreiben hinterlässt
entweder die alte oder die neue Notiz, niemals die Hälfte von beiden. Das
gleiche Verzeichnis ist Bedingung, weil `rename` über Dateisystemgrenzen
hinweg nicht atomar ist.

Der Watcher ignoriert `.sbtmp-*`-Ereignisse, damit die Temporärdatei nicht
indiziert wird.

### 11.5 `skipTouch`

Vier Operationen ändern Notizen, ohne `updated` zu setzen: das Nachziehen von
Links bei `note_move` und `note_merge`, `tag_rename`, `vault_replace` und
`note_restore`. Der Grund ist derselbe: Diese Änderungen sind Buchhaltung, nicht
Inhalt. Würden sie `updated` setzen, wäre nach einem einzigen Umbenennen die
Frage „was habe ich zuletzt bearbeitet?" für den halben Vault falsch
beantwortet.

### 11.6 Git

Ein Repository pro Vault. Beim ersten Öffnen wird es mit `git.PlainInit`
angelegt und eine `.gitignore` geschrieben, die `.secondbrain/`,
`.obsidian/workspace*.json` und `.DS_Store` ausschließt.

`Commit` sperrt das Repository, prüft den Status, bricht bei sauberem
Arbeitsverzeichnis ab, fügt alles hinzu und committet mit Author und Committer
aus der Konfiguration. Die Nachricht wird auf eine Zeile und 140 Zeichen
gekürzt und nennt Werkzeug und Pfad, zum Beispiel:

```
note_section_edit(append_to_section): wiki/rate-limiting.md
note_move: inbox/x.md -> wiki/x.md (3 links updated)
vault_replace: 12 notes, 31 occurrences
```

Ist `git_remote` gesetzt, wird nach jedem Commit in einer eigenen Goroutine
gepusht; ein Fehlschlag wird geloggt und blockiert den Schreibvorgang nicht.
Als Credential dient `git_token` über HTTP Basic mit dem Benutzernamen
`secondbrain`.

Kann ein Repository nicht geöffnet werden, startet der Vault trotzdem — mit
einer `git_unavailable`-Warnung und ohne `note_history`, `note_diff` und
`note_restore`. Ein Vault, der nicht versioniert werden kann, ist immer noch
ein benutzbarer Vault.

Die drei vault-weiten Werkzeuge `note_move`, `tag_rename` und `vault_replace`
schreiben ihre Einzeländerungen ohne `reason` und setzen am Ende **einen**
zusammenfassenden Commit. Ein Commit je berührter Datei würde die Historie
unbrauchbar machen.

## 12. Section-Edit-Semantik

### 12.1 Was eine Section ist

Eine Section beginnt bei ihrer Überschrift und endet **vor** der nächsten
Überschrift gleicher oder höherer Ebene. Unterabschnitte gehören dazu.

```markdown
## Aufbau          ← Beginn der Section "Aufbau"
Text
### Details        ← gehört dazu
Mehr Text
## Betrieb         ← Ende der Section "Aufbau"
```

Gibt es keine solche Überschrift mehr, reicht die Section bis zum Ende des
Rumpfes. Zeilennummern beziehen sich auf den Rumpf, nicht auf die Datei.

### 12.2 Wie eine Überschrift gefunden wird

`findSection` sucht in drei Durchgängen über die Überschriftenliste und nimmt
den ersten Treffer:

1. **Exakt**: `h.Text == want`
2. **Ohne Rücksicht auf Groß-/Kleinschreibung**: `strings.EqualFold`
3. **Präfix**: `h.Text` beginnt (kleingeschrieben) mit `want`

`want` ist die übergebene Überschrift, von der führende `#` und Leerzeichen
entfernt wurden. `## Offene Fragen` und `Offene Fragen` sind dasselbe.

Der Präfix-Durchgang macht `heading: "Offene"` auf `## Offene Fragen`
auffindbar. Er ist bewusst der letzte: eine exakte Übereinstimmung soll nie
von einem Präfixtreffer verdrängt werden. Wird nichts gefunden, ist die
Fehlermeldung ein Verweis auf `note_outline`.

### 12.3 Die acht Modi

| Modus | `heading` | `content` | Wirkung |
| --- | :---: | :---: | --- |
| `append_to_section` | ja | ja | Fügt am Ende der Section ein, vor der nächsten Überschrift, nach dem Entfernen abschließender Leerzeilen |
| `prepend_to_section` | ja | ja | Fügt direkt unter der Überschrift ein, vor dem bisherigen Inhalt |
| `replace_section` | ja | ja | Ersetzt den Inhalt; die Überschrift selbst bleibt stehen |
| `insert_before_section` | ja | ja | Fügt vor der Überschrift ein |
| `insert_after_section` | ja | ja | Fügt hinter dem Ende der Section ein, also vor der nächsten Überschrift gleicher oder höherer Ebene |
| `delete_section` | ja | nein | Entfernt Überschrift und Inhalt vollständig |
| `append_to_note` | nein | ja | Hängt an den Rumpf an, durch eine Leerzeile getrennt |
| `prepend_to_note` | nein | ja | Stellt dem Rumpf voran, durch eine Leerzeile getrennt |

`heading` ist für alle Modi außer `append_to_note` und `prepend_to_note`
Pflicht. `content` ist für alle Modi außer `delete_section` Pflicht.

Nach jeder Section-Operation werden Läufe von drei oder mehr Leerzeilen auf
zwei reduziert und der Rumpf mit genau einem `\n` abgeschlossen.
Section-Edits hinterlassen naturgemäß zusätzliche Leerzeilen, und die
sammeln sich sonst an.

Mit `create: true` wird eine fehlende Notiz angelegt: Frontmatter mit `title`
und leeren `tags`, eine `#`-Überschrift aus dem Dateinamen und, falls der
Modus eine Überschrift braucht, eine `##`-Überschrift mit dem gesuchten Text.
Erst danach läuft der eigentliche Section-Edit.

## 13. Werkzeugreferenz

34 Werkzeuge. Jedes hat ein JSON-Schema mit `"additionalProperties": false`.

**Das Argument `vault`** wird jedem Werkzeug außer `vault_list` und
`vault_create` automatisch hinzugefügt: ein optionaler String, der bestimmt,
in welchem Vault gearbeitet wird. Wird er weggelassen, gilt `default_vault`.
Ist der Name unbekannt oder für diesen User nicht freigegeben, ist die Meldung
in beiden Fällen dieselbe — ob ein Vault existiert, den man nicht anfassen
darf, geht den Aufrufer nichts an. `vault_list` und `vault_create` bestimmen
ihren Vault selbst und haben das Argument deshalb nicht.

**Argumentbehandlung.** Strings werden für die meisten Felder an den Rändern
beschnitten; `content`, `old_string`, `new_string`, `append` und `replace`
behalten ihre Zeichen unverändert, weil dort jedes Leerzeichen zählt.
Ganzzahlen werden aus JSON-Zahlen gelesen. Listenfelder akzeptieren ein Array
oder einen kommagetrennten String. Zeitangaben akzeptieren `YYYY-MM-DD`, einen
RFC-3339-Zeitstempel, `2006-01`, `today` oder eine Spanne wie `7d`, `2w`,
`6m`, `1y`, `12h`.

**Fehler**, die für jedes Werkzeug gelten: ungültiger Pfad (§9), unbekannter
Vault, `read only` bei einem `read_only`-User und einem mutierenden Werkzeug.
Alle werden als `isError`-Ergebnis geliefert (§7.5).

### 13.1 Discovery

#### `vault_list`

Keine Argumente außer den generischen.

**Rückgabe:** `{"vaults":[{"name","notes","attachments","words","bytes","last_modified","is_default","versioned","has_instructions"}],"default_vault"}`.
`versioned` sagt, ob für diesen Vault ein Git-Repository geöffnet werden
konnte. Es werden nur Vaults aufgeführt, die der User adressieren darf.

#### `vault_create` — mutierend

| Argument | Typ | Pflicht | Wirkung |
| --- | --- | :---: | --- |
| `name` | String | ja | Vault-Name, muss `^[a-z0-9][a-z0-9_-]{0,63}$` entsprechen |
| `layout` | Enum | nein | `wiki-raw` (Standard), `zettelkasten`, `para`, `empty` |

Legt das Verzeichnis an, erzeugt die Layout-Verzeichnisse mit je einer
`.gitkeep`, schreibt die Vorlagen nach `templates/`, schreibt
`.secondbrain/instructions.md`, öffnet Index und Git, führt einen Reconcile
durch, startet einen Watcher und committet `vault_create: <name>`.

**Rückgabe:** `{"vault","layout","directories","instructions","message"}`.

**Fehler:** ungültiger Name; Vault existiert bereits; der User darf diesen
Vault-Namen nicht verwenden; unbekanntes Layout.

#### `vault_stats`

Keine eigenen Argumente.

**Rückgabe:** `{"vault","stats":{…}}` mit `notes`, `attachments`, `words`,
`bytes`, `tags`, `links`, `broken_links`, `orphans`, `open_tasks`,
`last_modified`, `top_tags` (die zehn häufigsten als `"tag (n)"`).

Als verwaist gilt eine Notiz, auf die kein aufgelöster Link zeigt und die
selbst keinen aufgelösten Link enthält.

#### `note_list`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `prefix` | String | nein | ganzer Vault |
| `glob` | String | nein | — |
| `sort` | Enum | nein | `modified`; sonst `path` oder `size` |
| `limit` | Int | nein | 100, begrenzt auf 1–500 |
| `offset` | Int | nein | 0 |

Durchläuft den Vault (Anhänge eingeschlossen), filtert, sortiert, paginiert.
`glob` wird mit `path.Match` gegen den vollständigen relativen Pfad geprüft;
`*` überschreitet dabei keine `/`-Grenze.

**Rückgabe:** `{"vault","total","returned","entries":[{"path","bytes","modified"}]}`.

**Fehler:** ungültiges Glob-Muster.

#### `note_search`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `query` | String | nein | leer, dann reines Filtern |
| `tags` | Liste | nein | — (mehrere Tags werden und-verknüpft) |
| `prefix` | String | nein | — |
| `glob` | String | nein | — |
| `modified_after` | String | nein | — |
| `limit` | Int | nein | 25; Werte ≤ 0 oder > 200 ergeben ebenfalls 25 |
| `offset` | Int | nein | 0 |

**Rückgabe:** `{"vault","total","returned","hits":[{"path","title","score","snippet","tags","modified","bytes"}]}`,
bei `total == 0` zusätzlich ein `hint`, der auf weniger Wörter, weniger Filter
oder `vault_grep` verweist.

**Fehler:** ungültige FTS5-Syntax (in Klartext übersetzt); unlesbare
Zeitangabe.

#### `vault_grep`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `pattern` | String | ja | Go-Regexp (RE2) |
| `case_sensitive` | Bool | nein | `false`; sonst wird `(?i)` vorangestellt |
| `prefix` | String | nein | ganzer Vault |
| `context` | Int | nein | 1, begrenzt auf 0–5 |
| `limit` | Int | nein | 50, begrenzt auf 1–300 |
| `include_content` | Bool | nein | ohne Wirkung in v1 (§21) |

Liest jede Notiz unter 4 MiB, Zeile für Zeile. Ist das Limit erreicht, wird
der Durchlauf abgebrochen und `truncated` gesetzt.

**Rückgabe:** `{"vault","matches":[{"path","line","text","context"}],"notes_scanned","truncated"}`.
`context` enthält die umgebenden Zeilen als `"<nummer>: <text>"`, ohne die
Trefferzeile selbst.

**Fehler:** ungültiger regulärer Ausdruck.

### 13.2 Lesen

#### `note_read`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | `.md` optional |
| `heading` | String | nein | ganze Notiz |
| `with_metadata` | Bool | nein | `true` |

**Rückgabe:** `{"vault","path","title","content","content_hash","bytes","modified","frontmatter"}`
und, bei `with_metadata`, zusätzlich `headings`, `tags`, `links` sowie `tasks`
(nur wenn vorhanden). `content` ist der Rumpf **ohne** Frontmatter; mit
`heading` nur der Text dieser Section einschließlich der Überschriftenzeile.
`content_hash` bezieht sich immer auf die ganze Datei.

**Fehler:** nicht gefunden; Pfad ist ein Verzeichnis; Datei größer als 4 MiB;
Überschrift nicht gefunden.

#### `note_outline`

| Argument | Typ | Pflicht |
| --- | --- | :---: |
| `path` | String | ja |

**Rückgabe:** `{"vault","path","title","content_hash","bytes","words","modified","frontmatter","headings","tags","outgoing","incoming","open_tasks"}`.
Kein Fließtext. Das ist der Sinn: Erst die Form einer langen Notiz ansehen,
dann entscheiden, welche Section man wirklich braucht.

#### `note_backlinks`

| Argument | Typ | Pflicht |
| --- | --- | :---: |
| `path` | String | ja |

**Rückgabe:** `{"vault","path","incoming":[{"path","title","target","anchor"}],"outgoing":[…]}`,
dazu `broken` und ein `hint`, wenn Links ins Leere zeigen. Beantwortet aus dem
Index; die Notiz selbst muss nicht existieren.

#### `note_related`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `limit` | Int | nein | 10 |

**Rückgabe:** `{"vault","path","related":[{"path","title","score","tags","modified","bytes"}]}`,
absteigend nach dem Wert aus §10.6.

#### `tag_list`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `prefix` | String | nein | alle Tags |
| `limit` | Int | nein | 200, begrenzt auf 1–1000 |

**Rückgabe:** `{"vault","tags":[{"tag","notes"}],"count"}`, absteigend nach
Anzahl, bei Gleichstand alphabetisch. Frontmatter-Tags und Inline-Tags sind
zusammengeführt und normalisiert.

### 13.3 Schreiben

Alle Werkzeuge dieses Abschnitts liefern die gemeinsame Schreibantwort:

```json
{"path":"wiki/x.md","vault":"default","bytes":812,
 "content_hash":"9f2c…","created":true,"dry_run":true,
 "diff":"@@ -1,3 +1,5 @@\n …","message":"dry run: nothing was written"}
```

`created`, `deleted`, `dry_run`, `diff` und `message` erscheinen nur, wenn sie
zutreffen. `diff` ist ein Unified Diff mit drei Kontextzeilen und wird auch
nach einem tatsächlichen Schreibvorgang mitgeliefert — ein Agent, der Prosa
bearbeitet, hat sonst keine Möglichkeit festzustellen, ob das Geänderte auch
das Gemeinte war.

#### `note_create` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `title` | String | nein | Dateiname ohne Endung |
| `content` | String | nein | leer → `# <title>` |
| `tags` | Liste | nein | `[]` |
| `overwrite` | Bool | nein | `false` |
| `dry_run` | Bool | nein | `false` |

Schreibt Frontmatter mit `title`, `tags` und (über den gemeinsamen
Schreibpfad) `created` und `updated`.

**Rückgabe:** Schreibantwort, bei echtem Schreiben zusätzlich ein `hint`, der
daran erinnert, die neue Notiz zu verlinken.

**Fehler:** Es existiert bereits eine Notiz und `overwrite` ist nicht gesetzt.
Genau das ist der Zweck: erst suchen, und das Scheitern fängt das Duplikat ab,
das man gerade anlegen wollte.

#### `note_write` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `content` | String | ja | vollständiger neuer Rumpf |
| `content_hash` | String | **ja** | aus `note_read` |
| `keep_frontmatter` | Bool | nein | `true` |
| `dry_run` | Bool | nein | `false` |

Das einzige Werkzeug mit verpflichtendem `content_hash`. Ersetzt den Rumpf
vollständig; bei `keep_frontmatter` bleibt der bisherige Frontmatter-Block
erhalten, sonst hat die Notiz danach keinen — abgesehen von `created` und
`updated`, die der Schreibpfad ergänzt.

**Fehler:** `content_hash` fehlt (mit dem Hinweis, erst zu lesen); Hash stimmt
nicht mehr; Notiz existiert nicht.

#### `note_edit` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `old_string` | String | ja | muss zeichengenau passen |
| `new_string` | String | ja | leerer String löscht |
| `replace_all` | Bool | nein | `false` |
| `content_hash` | String | nein | optionaler Schutz |
| `dry_run` | Bool | nein | `false` |

**Rückgabe:** Schreibantwort plus `replacements` (Anzahl der Ersetzungen).

**Fehler:** `old_string` leer; `old_string` gleich `new_string`; nicht
gefunden; mehrfach gefunden ohne `replace_all` — die Meldung nennt die Anzahl
und fordert mehr Kontext. Mehrdeutigkeit wird abgelehnt, nicht geraten.

#### `note_section_edit` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `mode` | Enum | ja | die acht Modi aus §12.3 |
| `heading` | String | bedingt | Pflicht außer bei `append_to_note`/`prepend_to_note` |
| `content` | String | bedingt | Pflicht außer bei `delete_section` |
| `create` | Bool | nein | `false` |
| `content_hash` | String | nein | — |
| `dry_run` | Bool | nein | `false` |

**Fehler:** unbekannter Modus; fehlende Überschrift oder fehlender Inhalt für
den Modus; keine passende Überschrift gefunden (mit Verweis auf
`note_outline`); Notiz existiert nicht und `create` ist nicht gesetzt.

#### `note_frontmatter` — mutierend

| Argument | Typ | Pflicht | Wirkung |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `set` | Objekt | nein | Felder setzen; Schlüssel werden in sortierter Reihenfolge angewandt |
| `unset` | Liste | nein | Felder entfernen |
| `add_tags` | Liste | nein | Tags ergänzen, normalisiert |
| `remove_tags` | Liste | nein | Tags entfernen |
| `add_aliases` | Liste | nein | Aliase ergänzen |
| `content_hash` | String | nein | — |
| `dry_run` | Bool | nein | `false` |

Berührt den Rumpf nicht. `tags` und `aliases` werden als Menge geführt und
sortiert zurückgeschrieben. Inline-Tags im Text werden von diesem Werkzeug
nicht angefasst.

**Rückgabe:** Schreibantwort plus `frontmatter` mit dem Stand danach.

**Fehler:** kein einziges der fünf Änderungsargumente gesetzt; Notiz existiert
nicht.

#### `note_move` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | Quellpfad |
| `to` | String | ja | Zielpfad |
| `update_links` | Bool | nein | `true` |
| `overwrite` | Bool | nein | `false` |
| `dry_run` | Bool | nein | `false` |

Der Ablauf: Backlinks aus dem Index holen, daraus je verlinkender Notiz eine
Liste **literaler** Ersetzungen planen, die Datei umbenennen, Index für beide
Pfade aktualisieren, die geplanten Ersetzungen anwenden (mit `skipTouch`) und
am Ende einen zusammenfassenden Commit setzen.

Die Ersetzungen sind literal und nicht regulär, damit jede einzelne vorher
angezeigt werden kann. Geplant wird je nach Schreibweise des Links:

| Zielform im Link | Ersetzung |
| --- | --- |
| vollständiger Pfad mit `.md` | `[[<alt>` → `[[<neu>` |
| vollständiger Pfad ohne `.md` | `[[<alt>` → `[[<neu ohne .md>` |
| bloßer Dateiname | nur wenn sich der Dateiname ändert; sonst bleibt der Link gültig |
| Markdown-Link | `(<alt>` → `(<neu>` |

**Rückgabe:** bei `dry_run` `{"vault","from","to","dry_run","link_updates","message"}`;
sonst `{"vault","from","to","notes_updated","link_updates"}`.

**Fehler:** Quelle und Ziel identisch; Quelle nicht gefunden; Ziel existiert
und `overwrite` ist nicht gesetzt.

#### `note_delete` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `content_hash` | String | nein | — |
| `dry_run` | Bool | nein | `false` |

**Rückgabe:** Schreibantwort mit `deleted: true` und
`message: "moved to .secondbrain/trash"`, dazu `still_linked_from` und ein
`hint`, wenn noch Notizen auf sie zeigen.

**Fehler:** nicht gefunden; Hash stimmt nicht mehr.

#### `note_from_template` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `template` | String | nein | ohne Angabe: Vorlagen auflisten |
| `path` | String | nein | `inbox/YYYY-MM-DD-<slug>.md` |
| `title` | String | nein | Vorlagenname ohne Endung |
| `tags` | Liste | nein | zusätzlich zu den Tags der Vorlage |
| `dry_run` | Bool | nein | `false` |

Liest `templates/<template>.md` und ersetzt `{{title}}`, `{{date}}`
(`YYYY-MM-DD`), `{{time}}` (`HH:MM`), `{{datetime}}` (RFC 3339), `{{year}}`,
`{{month}}`, `{{day}}` und `{{slug}}`.

Ohne `template` wird `{"vault","templates":[…]}` geliefert, bzw. eine Meldung,
wenn es kein `templates/`-Verzeichnis gibt.

**Fehler:** Vorlage nicht gefunden (mit dem Hinweis, ohne Argument
aufzurufen); Zielpfad existiert bereits.

`Slug` bildet Kleinbuchstaben, ersetzt `ä ö ü ß` durch `ae oe ue ss`, ersetzt
jede andere Folge von Nicht-`[a-z0-9]` durch `-`, kürzt auf 80 Zeichen und
ergibt notfalls `note`.

### 13.4 Erfassen

#### `daily_note` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `date` | String | nein | heute; Format `YYYY-MM-DD` |
| `append` | String | nein | ohne Angabe wird nur gelesen |
| `heading` | String | nein | die aktuelle Uhrzeit als `HH:MM` |
| `dry_run` | Bool | nein | `false` |

Der Pfad ist in jedem Layout `journal/YYYY/YYYY-MM-DD.md`.

Ohne `append` und wenn die Notiz existiert, wird sie gelesen:
`{"vault","path","date","content","content_hash","existed":true}` — dann wird
nichts geschrieben.

Sonst wird sie bei Bedarf angelegt (Frontmatter `title`, `tags: [journal]`,
`date`; Rumpf `# Montag, 2. Januar 2006` im englischen Format
`Monday, 2 January 2006`). Existiert die Ziel-Überschrift bereits, wird an
diese Section angehängt, sonst wird der Block `## <heading>` mit dem Text ans
Ende der Notiz gehängt.

**Fehler:** `date` nicht im Format `YYYY-MM-DD`.

#### `inbox_capture` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `text` | String | ja | Markdown erlaubt |
| `title` | String | nein | erste Zeile, ohne `#-*> `, auf 60 Zeichen gekürzt |
| `tags` | Liste | nein | `inbox` wird immer ergänzt |
| `source` | String | nein | Herkunft; landet im Frontmatter |

Schreibt `inbox/YYYY-MM-DD-HHMM-<slug>.md` mit Frontmatter `title`, `tags`,
`captured` (RFC 3339) und optional `source`. Kein `dry_run`: das Werkzeug
existiert für den Fall, dass etwas nicht verloren gehen soll, und ein
Probelauf wäre dabei genau das falsche Angebot.

#### `daily_range`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `from` | String | nein | sechs Tage vor `to`; auch eine Spanne wie `7d` |
| `to` | String | nein | heute |
| `empty` | Bool | nein | `false` — Tage ohne Notiz weglassen |

**Rückgabe:** `{"vault","from","to","days_with_notes","days":[{"date","exists","path","content","content_hash"}]}`.

**Fehler:** unlesbares Datum; Zeitraum länger als 366 Tage. Liegt `from` nach
`to`, werden beide getauscht.

### 13.5 Pflege

#### `vault_review`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `stale_after` | String | nein | `365d`; nur Spannen (`d`, `w`, `m`, `y`, `h`) |
| `limit` | Int | nein | 15 je Kategorie |
| `only` | Enum | nein | `stub`, `orphan`, `broken_link`, `stale`, `open_task` |

Fünf Kategorien, jede aus einer eigenen Abfrage:

| Kategorie | Kriterium |
| --- | --- |
| `stub` | Notiz kleiner als 300 Bytes, kleinste zuerst |
| `orphan` | kein aufgelöster Link hin und keiner heraus |
| `broken_link` | `links.resolved = ''`, mit dem Zielnamen als Detail |
| `stale` | `mtime` älter als `stale_after`, älteste zuerst |
| `open_task` | jede unerledigte Checkbox, mit dem Text als Detail |

**Rückgabe:** `{"vault","stats","items":{"<kategorie>":[{"path","title","reason","detail"}]},"count"}`.

**Fehler:** `stale_after` ist keine Spanne.

#### `note_merge` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | die Notiz, die bleibt |
| `from` | String | ja | die Notiz, die aufgeht und verschwindet |
| `heading` | String | nein | Titel der Quellnotiz |
| `dry_run` | Bool | nein | `false` |

Ablauf: Beide Notizen lesen. An die Zielnotiz einen Block
`## <heading>` mit dem Rumpf der Quelle anhängen — die führende
`#`-Überschrift der Quelle wird dabei entfernt — samt HTML-Kommentar
`<!-- merged from <pfad> on <datum> -->`. Titel und Dateiname der Quelle
werden als `aliases` an der Zielnotiz ergänzt, damit alte `[[Links]]` weiter
auflösen. Anschließend wird die Quelle gelöscht (Papierkorb) und die Links
werden wie bei `note_move` nachgezogen.

**Rückgabe:** Schreibantwort plus `merged_from`, `backlinks_to_source` und
`links_updated`; bei `dry_run` stattdessen eine Meldung, was geschehen würde.

**Fehler:** eine der Notizen nicht gefunden; Quelle und Ziel identisch.
Schlägt das Löschen der Quelle nach erfolgreicher Zusammenführung fehl, sagt
die Fehlermeldung genau das — der Inhalt ist dann bereits übertragen.

#### `note_split` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `level` | Int | nein | 2, begrenzt auf 1–6 |
| `dir` | String | nein | Verzeichnis der Ausgangsnotiz |
| `dry_run` | Bool | nein | `false` |

Für jede Überschrift der gewählten Ebene wird eine neue Notiz
`<dir>/<slug>.md` erzeugt, mit Frontmatter `title`, den Tags der
Ausgangsnotiz und `source_note`, dem Sectiontext ohne führende
`#`-Überschrift und einer Rückverlinkung. Danach wird jede Section im Original
durch `See [[<neuer pfad>|<überschrift>]].` ersetzt. Kollidiert der Zielname
mit der Ausgangsnotiz, wird `-1` angehängt.

**Rückgabe:** bei `dry_run` `{"vault","path","dry_run","would_create","message"}`;
sonst die Schreibantwort für das Original plus `created` und `notes_created`.

**Fehler:** keine Überschrift der gewählten Ebene (mit Verweis auf
`note_outline`). Ein einzelnes Stück, dessen Zielpfad schon existiert, wird
übersprungen und geloggt; die übrigen werden erzeugt.

### 13.6 Aufgaben

#### `task_list`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `include_done` | Bool | nein | `false` |
| `prefix` | String | nein | ganzer Vault |
| `contains` | String | nein | Teilstring, ohne Rücksicht auf Groß-/Kleinschreibung |
| `limit` | Int | nein | 100; Werte ≤ 0 oder > 500 ergeben 100 |

**Rückgabe:** `{"vault","tasks":[{"path","line","text","done","note_title"}],"count"}`,
sortiert nach Pfad und Zeile. Die Zeilennummern stammen aus dem Index und
beziehen sich auf die Datei einschließlich Frontmatter.

#### `task_toggle` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `line` | Int | bedingt | Zeilennummer aus `task_list` |
| `text` | String | bedingt | exakter Aufgabentext, alternativ zu `line` |
| `done` | Bool | nein | `true` |
| `dry_run` | Bool | nein | `false` |

Genau eines von `line` und `text` muss gesetzt sein. Beim Schreiben wird die
Zeile als `<einrückung>- [x] <text>` bzw. `- [ ] <text>` neu gesetzt; ein
`*`- oder `+`-Aufzählungszeichen wird dabei zu `-`.

**Rückgabe:** Schreibantwort plus `task` und `done`.

**Fehler:** weder `line` noch `text`; Zeilennummer außerhalb der Datei; die
Zeile ist keine Checkbox (mit dem Hinweis, `task_list` für aktuelle
Zeilennummern aufzurufen); kein oder mehr als ein Treffer für `text`.

### 13.7 Umbauten

#### `tag_rename` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `from` | String | ja | mit oder ohne `#` |
| `to` | String | ja | ein bestehender Tag führt zur Verschmelzung |
| `dry_run` | Bool | nein | **`true`** |

Läuft über alle indizierten Notizen, überspringt jede, die den Tag nicht
trägt, und ändert bei den übrigen sowohl `tags` im Frontmatter als auch
Inline-Vorkommen. Das Muster verlangt vor dem `#` einen Zeilenanfang oder ein
Zeichen, das weder Wortzeichen noch Backtick noch `#` noch `/` ist, und dahinter
eine Wortgrenze. `updated` wird nicht
angefasst. Bei `dry_run: false` folgt ein zusammenfassender Commit.

**Rückgabe:** `{"vault","from","to","dry_run","notes":[{"path","diff"}],"count","message"}`.
Der `diff` je Notiz erscheint nur im Probelauf.

**Fehler:** `from` oder `to` leer; beide gleich.

#### `vault_replace` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `pattern` | String | ja | Literal oder Regexp |
| `replace` | String | ja | bei `regex` sind `$1` usw. Gruppenverweise |
| `regex` | Bool | nein | `false` |
| `prefix` | String | nein | ganzer Vault |
| `limit` | Int | nein | 50, begrenzt auf 1–500 |
| `dry_run` | Bool | nein | **`true`** |

Ersetzt im **gesamten Dateiinhalt**, Frontmatter eingeschlossen. `updated`
wird nicht angefasst. Bei `dry_run: false` folgt ein zusammenfassender Commit.

**Rückgabe:** `{"vault","dry_run","notes":[{"path","matches","diff"|"content_hash"|"error"}],"notes_affected","occurrences","message"}`.

**Fehler:** ungültiger regulärer Ausdruck; mehr als `limit` betroffene Notizen.
Der Aufruf bricht dann mit der Aufforderung ab, einen `prefix` zu setzen oder
das Limit bewusst zu erhöhen. Im Probelauf ist damit nichts geschehen; bei
`dry_run: false` sind die bis dahin bearbeiteten Notizen bereits geschrieben
und committet (§21). Genau deshalb ist der Probelauf hier der Standard. Der Standard `dry_run: true` ist
Absicht: ein vault-weites Ersetzen ist der einfachste Weg, eine Wissensbasis
zu beschädigen.

#### `attachment_list`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `prefix` | String | nein | `attachments` |
| `limit` | Int | nein | 100, begrenzt auf 1–500 |

**Rückgabe:** `{"vault","attachments":[{"path","bytes","modified","references"}],"count"}`,
nach Pfad sortiert. `references` ist die Zahl der Notizen, die aufgelöst auf
die Datei zeigen.

#### `attachment_put` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | Zielpfad im Vault |
| `data` | String | ja | Dateiinhalt, base64 (Standardalphabet) |
| `overwrite` | Bool | nein | `false` |

Prüft die Endung gegen die Positivliste aus §8.6, dekodiert, prüft die Grenze
von 32 MiB, schreibt, aktualisiert den Index und committet
`attachment_put: <pfad>`.

**Rückgabe:** `{"vault","path","bytes","markdown"}` — `markdown` ist ein
fertiges `![name](pfad)` zum Einfügen.

**Fehler:** Endung nicht erlaubt (die Meldung nennt die erlaubten); kein
gültiges base64; über 32 MiB; Datei existiert und `overwrite` ist nicht
gesetzt.

Kein `dry_run` und kein Papierkorb: bei `overwrite: true` ist der bisherige
Inhalt nur über Git wiederherstellbar (§21).

### 13.8 Historie

Alle drei Werkzeuge verlangen ein geöffnetes Git-Repository. Fehlt es, lautet
die Meldung, dass die Versionierung für diesen Vault aus ist.

#### `note_history`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `limit` | Int | nein | 20; Werte ≤ 0 oder > 100 ergeben 20 |

**Rückgabe:** `{"vault","path","revisions":[{"commit","when","author","message"}]}`,
neueste zuerst. `commit` sind die ersten zwölf Zeichen des Hashes, `message`
die erste Zeile — also der Name des Werkzeugs und der Pfad.

**Fehler:** noch keine Commits.

#### `note_diff`

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `from` | String | nein | die vorletzte Revision der Notiz |
| `to` | String | nein | der Stand auf der Platte |

Ohne `to` wird gegen die Arbeitskopie verglichen und `to` als
`"working copy"` zurückgegeben. Sind beide Stände gleich, lautet der Diff
`(no difference)`.

**Rückgabe:** `{"vault","path","from","to","diff"}`.

**Fehler:** unbekannte Revision; die Notiz existiert in dieser Revision nicht;
noch keine Historie.

#### `note_restore` — mutierend

| Argument | Typ | Pflicht | Standard |
| --- | --- | :---: | --- |
| `path` | String | ja | — |
| `revision` | String | ja | Commit-Id aus `note_history` |
| `dry_run` | Bool | nein | `false` |

Holt den Inhalt aus der Revision und schreibt ihn über den gewöhnlichen
Schreibpfad zurück. Der bisherige Stand geht damit in den Papierkorb und
bleibt als vorheriger Commit erhalten. `updated` wird nicht verändert, weil
der wiederhergestellte Stand seinen eigenen Zeitstempel mitbringt.

**Rückgabe:** Schreibantwort plus `restored_from`.

**Fehler:** unbekannte Revision; die Notiz existiert in dieser Revision nicht.

## 14. Fehler

### 14.1 Ebenen

| Ebene | Form | Beispiel |
| --- | --- | --- |
| Transport | HTTP-Status mit `{"error","status"}` | `401` ohne Token, `403` bei falschem Origin, `429` bei Ratenlimit |
| Protokoll | JSON-RPC-Fehlerobjekt | `-32601` bei unbekannter Methode |
| Werkzeug | Ergebnis mit `isError: true` | „the note changed since it was read: …" |

Die dritte Ebene ist die für ein Modell wichtigste, deshalb sind die Meldungen
dort als Anweisungen formuliert: Sie sagen nicht nur, was schiefging, sondern
welchen Aufruf man stattdessen machen sollte.

### 14.2 Wiederkehrende Werkzeugfehler

| Text (Anfang) | Bedeutung |
| --- | --- |
| `note not found: …` | Pfad existiert nicht |
| `a note already exists at that path: …` | Kollision beim Anlegen |
| `invalid path: …` | §9 verletzt, inklusive versteckter Pfade |
| `unknown vault: …` | Vault existiert nicht oder ist für diesen User gesperrt |
| `the note changed since it was read: …` | `content_hash` passt nicht mehr |
| `this user may not modify the vault` | `read_only` |
| `old_string appears N times; …` | mehrdeutiger Ausschnitt |
| `no heading matching "…"` | Section nicht gefunden |
| `versioning is off for this vault: …` | Git fehlt |

## 15. Audit-Log

Eine JSON-Zeile je Ereignis auf stdout, mit `ts` (Millisekunden, UTC),
`level` und `event`.

### 15.1 Tool-Aufrufe

```json
{"ts":"2026-07-30T09:14:02.183Z","level":"info","event":"tool_call",
 "tool":"note_section_edit","user":"andreas","vault":"default",
 "path":"wiki/rate-limiting.md","dry_run":true,
 "duration_ms":7,"results":0,"bytes":412}
```

Es wird **eine** Zeile je Aufruf geschrieben, ob er gelungen ist oder nicht.
Bei einem Fehler enthält sie `error` mit dem Meldungstext und keine
Größenangaben. `bytes` ist die Größe des kodierten Ergebnisses, nicht die der
Notiz; `truncated` erscheint, wenn `max_response_bytes` gegriffen hat.

**Notizinhalte erscheinen in keinem Ereignis, auf keinem Loglevel.** Ein
Audit-Log, das die Notizen zitiert, wäre eine zweite Kopie des Vaults im
stdout des Containers — und stdout landet in einer Logdatei, einem
Log-Aggregator und einem Backup, das niemand als vertraulich behandelt.
Protokolliert wird, *was angefasst wurde*, nie *was drin steht*.

### 15.2 Weitere Ereignisse

`startup`, `shutdown`, `vault_created`, `index_reconciled`,
`index_ingest_failed`, `index_update_failed`, `reconcile_failed`,
`watch_error`, `watch_unavailable`, `git_unavailable`, `git_commit_failed`,
`git_push_failed`, `git_remote_failed`, `link_rewrite_failed`,
`split_piece_failed`, `trash_purged`, `config_reloaded`,
`config_reload_rejected`, `config_reload_partial`, `config_error`,
`client_registered`, `login_success`, `login_failed`, `login_stale_form`,
`token_issued`, `token_failed`, `token_reuse_detected`, `token_evicted`,
`origin_refused`, `startup_failed`.

`login_failed` enthält niemals den versuchten Benutzernamen — sonst stünde bei
einem Vertipper das Passwort im Log.

## 16. Metriken

Ein Prometheus-Endpunkt im Textformat `0.0.4`, standardmäßig **aus**. Eine
Wissensbasis ist kein öffentlicher Dienst, und ein Endpunkt, der ausgibt, wie
viele Notizen jemand hat und wie viel er schreibt, gehört nicht ungefragt auf
einen Listener. Ist er eingeschaltet, gibt es zwei voneinander unabhängige
Wege, ihn privat zu halten — einen gemeinsamen Schlüssel und einen eigenen
Listener —, und sie lassen sich kombinieren.

### 16.1 Umgebungsvariablen

| Variable | Typ | Standard | Wirkung |
| --- | --- | --- | --- |
| `SECONDBRAIN_METRICS` | Bool | `false` | Schaltet den Endpunkt ein. Gelesen mit `strconv.ParseBool`; nicht parsbar → Startfehler. |
| `SECONDBRAIN_METRICS_PATH` | Pfad | `/metrics` | Pfad, unter dem exponiert wird. |
| `SECONDBRAIN_METRICS_KEY` | String | — | Gemeinsamer Schlüssel. Literal, `env:NAME` oder `file:/pfad` (§3.4). Mindestens 16 Zeichen. |
| `SECONDBRAIN_METRICS_LISTEN` | String | — | Eigene Adresse, z. B. `:9090`. Gesetzt heißt: **nur** dort. |

### 16.2 YAML-Felder

| Feld | Typ | Standard | Wirkung |
| --- | --- | --- | --- |
| `metrics` | Bool | `false` | wie `SECONDBRAIN_METRICS` |
| `metrics_path` | String | `/metrics` | wie `SECONDBRAIN_METRICS_PATH` |
| `metrics_key` | String | `""` | wie `SECONDBRAIN_METRICS_KEY`; die Auflösung von `env:` und `file:` gilt auch hier |
| `metrics_listen` | String | `""` | wie `SECONDBRAIN_METRICS_LISTEN` |

Wie bei allen Serverwerten schlägt die Umgebung die Datei (§3).

### 16.3 Validierung

Geprüft wird nur, wenn `metrics` wahr ist; bei ausgeschaltetem Endpunkt bleiben
die übrigen drei Werte unbeachtet. Der Start scheitert, wenn:

- `metrics_path` nicht mit `/` beginnt
- `metrics_path` gleich `/mcp`, `/healthz` oder `/` ist
- `metrics_key` gesetzt und nach der Auflösung kürzer als 16 Zeichen ist
- eine `env:`- oder `file:`-Referenz in `metrics_key` nicht auflösbar ist

Kein Startfehler, aber eine Warnung: sind weder `metrics_key` noch
`metrics_listen` gesetzt, wird einmal beim Start `metrics_unprotected` auf
`warn` geloggt, mit dem Pfad und dem Hinweis auf beide Abhilfen. Der Prozess
startet trotzdem. Ein Startverbot wäre eine Behauptung über eine Umgebung, die
der Prozess nicht kennt — in einem abgeschotteten Netz ist genau diese
Konfiguration richtig.

`secondbrain validate` (§17) gibt den resultierenden Zustand als eine Zeile
aus: Pfad, Listener (`the main listener` oder die konfigurierte Adresse) und ob
ein Schlüssel verlangt wird. Ist der Endpunkt aus, steht dort `metrics: off`.
Der Schlüsselwert selbst wird nicht ausgegeben.

### 16.4 Authentisierung des Endpunkts

Ein Schlüssel ist optional, weil er auf einem privaten Listener nichts kauft.
Ist einer gesetzt, wird er strikt durchgesetzt, und zwar in zwei Schreibweisen:

- `Authorization: Bearer <key>` — das Präfix wird ohne Rücksicht auf
  Groß- und Kleinschreibung erkannt, der Wert dahinter wird getrimmt
- `X-API-Key: <key>` — für Scraper, denen das leichter fällt

Beide Vergleiche laufen über `subtle.ConstantTimeCompare`. Das Verhalten des
Endpunkts vollständig:

| Bedingung | Antwort |
| --- | --- |
| `metrics` aus | `404` — auch dann, wenn die Route beim Start eingehängt wurde |
| Methode weder `GET` noch `HEAD` | `405` |
| Ratenlimit überschritten | `429` |
| Schlüssel gesetzt, nicht oder falsch präsentiert | `401` mit `WWW-Authenticate: Bearer realm="secondbrain metrics"` |
| sonst | `200`, `Content-Type: text/plain; version=0.0.4; charset=utf-8`, `Cache-Control: no-store` |

Die Reihenfolge ist bewusst so: erst der Schalter, dann die Methode, dann das
Ratenlimit, dann der Schlüssel. Ein `HEAD` bekommt Status und Header, aber
keinen Rumpf.

### 16.5 Das eigene Ratenlimit

Der Endpunkt hat einen **eigenen** Token-Bucket, `60/m` je Quell-IP, fest
verdrahtet und von den Limits aus §5.8 unabhängig. Zwei Gründe, und beide
zählen: ein Scraper mit falschem Schlüssel soll nicht als Timing-Orakel
taugen, und er soll den Login-Limiter nicht leerlaufen lassen können, der
sonst denselben Schlüsselraum — die Quell-IP — benutzt. Ein üblicher Scrape
alle 15 Sekunden liegt bei `4/m` und damit weit darunter.

### 16.6 Einhängen und der zweite Listener

Wo die Route liegt, wird **beim Start** entschieden:

- `metrics: true` und `metrics_listen` leer → die Route wird unter
  `metrics_path` auf dem Hauptlistener registriert, neben `/mcp`.
- `metrics: true` und `metrics_listen` gesetzt → die Route wird auf dem
  Hauptlistener **nicht** registriert. Stattdessen läuft ein zweiter
  `http.Server` auf der angegebenen Adresse, der `metrics_path` bedient und
  zusätzlich ein `/healthz` mit `{"status":"ok"}`.

Der Unterschied ist mehr als kosmetisch. Auf dem Hauptlistener existiert die
Route dann gar nicht, also antwortet er ein echtes `404` statt eines `401` —
ein `401` verrät, dass es an dieser Stelle etwas zu holen gibt. Und ein Port,
der im Container-Netz gebunden und nie veröffentlicht wird, ist von außen
nicht erreichbar, gleichgültig wie gut der Schlüssel ist; das ist die
wirksamere der beiden Absicherungen.

Der zweite Listener trägt dieselben Sicherheitsheader wie der Hauptlistener
(`X-Content-Type-Options`, `Referrer-Policy`), läuft aber **nicht** durch die
zählende Middleware: seine Anfragen erscheinen nicht in
`secondbrain_http_requests_total`. Beim Herunterfahren wird er mit dem Prozess
geschlossen. Scheitert das Binden der Adresse, wird `metrics_listener_failed`
geloggt; der Hauptlistener läuft weiter.

Daraus folgt das Verhalten bei einem Reload (§4): `metrics_path` und
`metrics_listen` wirken **erst nach einem Neustart**, weil ein Reload die
Konfiguration tauscht, aber keine Routen neu einhängt. Einschalten heißt also
neu starten, nicht `SIGHUP` schicken. `metrics` und `metrics_key` liest der
Handler dagegen bei jeder Anfrage aus der aktuellen Konfiguration; ein
Abschalten oder ein Schlüsselwechsel greift damit sofort, wobei eine bereits
eingehängte Route nach dem Abschalten mit `404` antwortet.

Ein lauffähiges Beispiel — secondbrain und ein Prometheus auf einem
gemeinsamen internen Netz, Metriken auf dem nicht veröffentlichten zweiten
Listener — liegt unter `deploy/prometheus/`.

### 16.7 Erhebung

Die Registry in `metrics.go` ist handgeschrieben (§16.9) und hält ihre Zähler
im Arbeitsspeicher. Ein Neustart setzt sie auf null zurück; für Prometheus ist
das der Normalfall, `rate()` erkennt den Rücksprung.

- **Werkzeugzahlen** werden im Audit-Pfad erhoben, an derselben Stelle wie die
  Logzeile. Eine Metrik kann dem Audit-Log damit nicht widersprechen.
- **HTTP-Zahlen** kommen aus einer Middleware um den Hauptlistener.
- **Vault-Kennzahlen** werden **zum Scrape-Zeitpunkt** aus dem Index geholt
  (§10). Ein Scrape ist damit eine Handvoll zählender SQLite-Abfragen und kein
  Lauf über das Dateisystem. Ein Vault, dessen Statistik fehlschlägt, wird
  übersprungen, statt den ganzen Scrape scheitern zu lassen.
- **Sitzungs- und Tokenzahlen** kommen aus dem Sitzungsspeicher (§6).
- Eine Familie **ohne Reihen wird ganz weggelassen**, samt `# HELP` und
  `# TYPE`. Vor dem ersten Werkzeugaufruf gibt es also keine
  `secondbrain_tool_calls_total`, und ohne Vault keine `secondbrain_vault_*`.

### 16.8 Die Metriken

Gauges:

| Name | Labels | Bedeutung |
| --- | --- | --- |
| `secondbrain_build_info` | `version`, `commit` | Immer `1`; die Information steckt in den Labels. |
| `secondbrain_uptime_seconds` | — | Sekunden seit Prozessstart, ganzzahlig ausgegeben. |
| `secondbrain_users` | — | Konfigurierte Benutzer. |
| `secondbrain_oauth_clients` | — | Seit dem Start registrierte OAuth-Clients. Nur im Speicher. |
| `secondbrain_access_tokens` | — | Lebende Access Tokens. |
| `secondbrain_refresh_tokens` | — | Lebende Refresh Tokens. |
| `secondbrain_mcp_sessions` | — | Offene MCP-Sitzungen. |
| `secondbrain_vault_notes` | `vault` | Markdown-Notizen im Vault. |
| `secondbrain_vault_words` | `vault` | Wörter über alle Notizen. |
| `secondbrain_vault_bytes` | `vault` | Bytes über alle Notizen. |
| `secondbrain_vault_tags` | `vault` | Verschiedene Tags. |
| `secondbrain_vault_links` | `vault` | Interne Links. |
| `secondbrain_vault_broken_links` | `vault` | Links auf eine Notiz, die es nicht gibt. |
| `secondbrain_vault_orphans` | `vault` | Notizen ohne eingehenden und ohne ausgehenden Link. |
| `secondbrain_vault_open_tasks` | `vault` | Nicht abgehakte Aufgabenkästchen. |
| `secondbrain_vault_attachments` | `vault` | Dateien im Vault, die keine Notizen sind. |

Counter:

| Name | Labels | Bedeutung |
| --- | --- | --- |
| `secondbrain_tool_calls_total` | `tool`, `outcome` | Werkzeugaufrufe. `outcome` ist `ok`, `error` oder `denied`. |
| `secondbrain_tool_duration_seconds_total` | `tool` | Aufsummierte Laufzeit je Werkzeug, mit drei Nachkommastellen. |
| `secondbrain_tool_result_bytes_total` | `tool` | Aufsummierte Ergebnisgröße je Werkzeug. |
| `secondbrain_http_requests_total` | `path`, `status` | Anfragen auf dem Hauptlistener. `path` stammt aus einer festen Liste, siehe §16.9. |
| `secondbrain_logins_total` | `outcome` | `success`, `failed` oder `rate_limited`. |
| `secondbrain_writes_total` | — | Mutierende Aufrufe, die tatsächlich geschrieben haben: `ok` und kein Probelauf. |
| `secondbrain_dry_runs_total` | — | Mutierende Aufrufe, die Probeläufe waren. |
| `secondbrain_truncated_results_total` | — | Ergebnisse, die an `max_response_bytes` gekürzt wurden. |
| `secondbrain_index_updates_total` | — | Aktualisierungen des Index für einen einzelnen Pfad. |
| `secondbrain_index_reconciles_total` | — | Vollständige Abgleiche des Index. |
| `secondbrain_index_errors_total` | — | Fehlgeschlagene Indexoperationen. |
| `secondbrain_watch_events_total` | — | Vom Watcher aufgenommene Dateisystemänderungen. |
| `secondbrain_git_commits_total` | — | Erzeugte Commits. |
| `secondbrain_git_failures_total` | — | Fehlgeschlagene Commits oder Pushes. |
| `secondbrain_trash_purged_total` | — | Nach Ablauf von `trash_retention` entfernte Papierkorbkopien. |

Drei Beobachtungen, für die diese Zahlen gedacht sind: steigende
`secondbrain_vault_broken_links` und `secondbrain_vault_orphans` über Wochen
sind eine verfallende Wissensbasis; ein flaches `secondbrain_vault_notes` bei
steigendem `secondbrain_writes_total` heißt, dass ein Agent ändert, aber
nichts anlegt; ein steigendes `secondbrain_truncated_results_total` heißt,
dass `max_response_bytes` für die Art der Abfragen zu klein ist.

### 16.9 Kardinalität, und was bewusst nicht exponiert wird

**Keine Client-Bibliothek.** Die Exposition ist ein paar Dutzend Zeilen aus
`# HELP`, `# TYPE` und Zahlen; sie von Hand zu schreiben kostet weniger, als
die offizielle Go-Client-Bibliothek samt ihrem Abhängigkeitsbaum in einen
Container zu ziehen, in dem die privaten Notizen von jemandem liegen. Es ist
dieselbe Abwägung, die die Abhängigkeitsliste des Projekts insgesamt kurz hält
(§2). Bezahlt wird sie mit dem, was in §21 steht: keine Histogramme.

**Niedrige Kardinalität.** Jedes Label stammt aus einer festen oder sehr
kleinen Menge:

| Label | Wertebereich |
| --- | --- |
| `tool` | die 34 Werkzeugnamen |
| `outcome` | `ok`, `error`, `denied` bzw. `success`, `failed`, `rate_limited` |
| `status` | die tatsächlich auftretenden HTTP-Status |
| `path` | eine feste Liste: `/healthz`, `/mcp`, `/register`, `/authorize`, `/token`, `/favicon.ico`, `/favicon.svg`, `/logo.svg`, `/`, `/.well-known`, `metrics`, `other` |
| `vault` | die Vaults im Datenverzeichnis, vom Betreiber bestimmt |
| `version`, `commit` | je Build ein Wert |

`path` wird **nie** aus `r.URL.Path` übernommen, sondern auf diese Liste
abgebildet; alles Unbekannte wird zu `other`. Ein Label aus einer
Benutzereingabe ist der Weg, auf dem ein Metrikendpunkt zur Waffe gegen seine
eigene Zeitreihendatenbank wird: wer Pfade erfinden darf, erfindet Zeitreihen.
Die niedrige Kardinalität ist damit zugleich eine Datenschutzeigenschaft und
der Unterschied zwischen einer Datenbank, die trägt, und einer, die umfällt.

Bewusst **nicht** enthalten:

- **Kein Notizinhalt, kein Pfad, kein Titel, kein Tag.** Keine Metrik trägt
  etwas aus einer Notiz. `secondbrain_vault_tags` zählt Tags, es nennt keinen.
- **Kein Benutzername.** Weder als Label noch als eigene Reihe.
  `secondbrain_logins_total` unterscheidet nur nach Ausgang; *wer* sich
  angemeldet hat, steht im Audit-Log (§15) und nicht in einer Zeitreihe, die
  auf einem Dashboard landet.
- **Keine Zeitstempel je Notiz.** Es gibt kein „zuletzt geschrieben am". Das
  wäre ein Aktivitätsprotokoll mit anderem Namen.
- **Keine Histogramme und keine Quantile.** Dauern gibt es nur als Summe je
  Werkzeug. `rate(…_duration_seconds_total[5m]) / rate(…_calls_total[5m])`
  ergibt einen Mittelwert; Perzentile ergibt es nicht.
- **Keine Zähler je Fehlermeldung.** Meldungstexte enthalten Pfade, und ein
  Pfad ist Notizinformation.
- **Keine Exemplars, kein Push, kein OpenMetrics-Format.** Es gibt genau die
  Textausgabe für einen Pull-Scrape.
- **Keine Persistenz.** Die Zähler liegen im Speicher und beginnen nach einem
  Neustart bei null.

## 17. CLI

| Befehl | Wirkung |
| --- | --- |
| `secondbrain` | Startet den Server |
| `secondbrain validate [pfad]` | Lädt und validiert die Konfiguration, gibt aus, was sie bedeutet, und endet bei Fehlern mit Rückgabewert 1 |
| `secondbrain reindex [pfad]` | Öffnet jeden Vault, gleicht den Index vollständig ab und gibt je Vault Notizen, Wörter, Tags und gebrochene Links aus |
| `secondbrain hashpw` | Liest ein Passwort (TTY mit Wiederholung, sonst von stdin) und gibt `bcrypt:<hash>` mit Kostenfaktor 12 aus |
| `secondbrain version` | Version, Commit, Build-Zeitpunkt |
| `secondbrain help` | Kurzhilfe mit den Umgebungsvariablen |

Ohne Pfadargument gilt `SECONDBRAIN_CONFIG`, sonst
`/etc/secondbrain/config.yaml`.

`validate` gibt `public_url`, `listen`, `data_dir`, `default_vault`, den
Git-Zustand, die erlaubten Origins und je Benutzer die Vault-Reichweite, den
Zugriffsmodus und die Art des Passworts aus. **Ein Passwortwert wird nicht
ausgegeben** — nur, ob es ein Klartext oder ein bcrypt-Hash ist.

## 18. Betrieb

### 18.1 Container

Mehrstufiger Build aus `golang:1.25-alpine`, Laufzeit
`gcr.io/distroless/static-debian12:nonroot`, gebaut mit `-trimpath` und
`-ldflags "-s -w -X main.version=… -X main.commit=… -X main.built=…"`.

- `EXPOSE 2020`, `VOLUME /data`, `USER nonroot:nonroot`
- Voreingestellte Umgebung: `SECONDBRAIN_CONFIG`, `SECONDBRAIN_LISTEN`,
  `SECONDBRAIN_DATA`, `SECONDBRAIN_DEFAULT_VAULT`
- Empfohlen: `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`
- **Nicht möglich:** `read_only: true`. Der Prozess schreibt die Notizen, den
  Index und ein Git-Repository je Vault.

### 18.2 UID 65532

Der `nonroot`-Benutzer der distroless-Images ist **UID 65532**. Ein Named
Volume erbt die Rechte beim ersten Start und funktioniert ohne Zutun. Ein
Bind Mount tut das nicht:

```bash
mkdir -p ./data && sudo chown -R 65532:65532 ./data
```

Fehlt dieser Schritt, scheitert der Start beim Anlegen des Datenverzeichnisses
oder beim Öffnen des Index. Das ist der mit Abstand häufigste Grund, aus dem
eine erste Inbetriebnahme fehlschlägt.

### 18.3 Volumes und Sicherung

Alles Dauerhafte liegt unter `/data`. Eine Sicherung ist ein Kopieren dieses
Verzeichnisses; `.secondbrain/index.db` darf dabei ausgelassen werden, weil
der Index sich selbst neu baut. Das Verzeichnis lässt sich im laufenden
Betrieb kopieren; im ungünstigsten Fall fehlt die allerletzte Änderung, weil
`rename` und `rsync` gegeneinander laufen.

Die zweite Sicherungsebene ist `git_remote`: jeder Commit wird in ein privates
Repository geschoben, und damit liegt die vollständige Historie an einem
zweiten Ort, ohne dass jemand ein Backup eingerichtet hat.

Ein Vault ist auch ohne diesen Container nutzbar. `docker run --rm -v
secondbrain-data:/data alpine tar cf - /data` holt die Dateien heraus; danach
ist es ein gewöhnliches Verzeichnis Markdown, das man in Obsidian öffnen kann.

### 18.4 Healthcheck

`GET /healthz` liefert unauthentifiziert und ohne Ratenlimit
`{"status":"ok","version":…,"vaults":…}`.

Der Healthcheck im Compose-File benutzt stattdessen `/secondbrain version`,
weil das distroless-Image weder Shell noch `curl` enthält. Er prüft damit,
dass der Prozess ausführbar ist, nicht dass er antwortet; wer mehr will,
prüft `/healthz` vom Reverse Proxy aus.

### 18.5 Reverse Proxy

secondbrain terminiert kein TLS. `public_url` MUSS die Adresse sein, unter der
Clients den Server erreichen, denn jeder OAuth-Endpunkt und jede Weiterleitung
wird daraus abgeleitet. Beispiele für Traefik und einen Cloudflare Tunnel
liegen unter `deploy/`.

### 18.6 Verhalten bei Konfigurationsänderung

Siehe §4. Zusammengefasst: Benutzer, Limits, Origins und Git-Einstellungen
greifen ohne Neustart; `data_dir` und `public_url` nicht. Eine fehlerhafte
Datei lässt die laufende Konfiguration unangetastet, weil ein Neustart alle
Anmeldungen entwerten würde.

## 19. Quelltextaufbau

```
src/
├── main.go          Einstiegspunkt, CLI, Routing, geordnetes Herunterfahren
├── config.go        Umgebung und YAML, Auflösung, Validierung, Raten
├── reload.go        Datei-Watcher und SIGHUP, atomarer Tausch
├── oauth.go         Discovery, DCR, /token, PKCE, Fehlerhelfer
├── login.go         Anmeldeseite, Passwortprüfung, CSP
├── session.go       Clients, Codes, Tokens, CSRF, Janitor
├── mcp.go           Streamable HTTP, JSON-RPC, MCP-Sitzungen, instructions
├── tools.go         Registry, Schemata, Dispatch, Argumentzugriff
├── tools_read.go    Discovery und Lesen (11 Werkzeuge)
├── tools_write.go   Anlegen, Ändern, Erfassen (10 Werkzeuge)
├── tools_maint.go   Pflege, Umbauten, Historie (13 Werkzeuge)
├── vault.go         Vault-Verwaltung, Pfadauflösung, Walk
├── note.go          Markdown- und Frontmatter-Parsing, Struktur
├── edit.go          Schreibpfad, Papierkorb, Section-Edits, Vorlagen
├── index.go         SQLite-FTS5-Schema, Ingest, Abfragen
├── watch.go         fsnotify mit Entprellung
├── layouts.go       Layouts, Instruktionstexte, Vorlagen
├── gitstore.go      go-git: Init, Commit, Push, History, Contents
├── diff.go          Unified Diff
├── ratelimit.go     Token-Buckets, keyed limiter
├── metrics.go       Registry aus Zählern und Gauges, Textausgabe
├── metrics_http.go  Metrikendpunkt, Schlüsselprüfung, zweiter Listener
├── audit.go         strukturiertes Logging, AuditRecord
└── favicon.go       eingebettetes Icon für die Anmeldeseite
```

Die Aufteilung der Werkzeuge auf drei Dateien folgt dem Lebenszyklus einer
Notiz, nicht der Alphabetik: finden, schreiben, pflegen. Wer ein Werkzeug
sucht, sucht es in der Phase, in der er gerade denkt.

## 20. Testanforderungen

Die Unit-Tests liegen in `src/note_test.go`, `src/vault_test.go` und
`src/tools_test.go`. Abgedeckt sein MUSS mindestens:

- **Pfadauflösung** — `..`, absolute Pfade, Backslashes, Nullbytes, leere
  Segmente, jeder mit einem Punkt beginnende Bestandteil, Symlink aus dem
  Vault heraus, Ergänzung der `.md`-Endung.
- **Frontmatter** — Block nur am Dateianfang, unterminierter Block, `...` als
  Abschluss, Erhalt von Reihenfolge und Kommentaren über einen Zyklus,
  `tags` als Liste und als Skalar, BOM.
- **Struktur** — Überschriften und Aufgaben innerhalb von Codeblöcken werden
  ignoriert; Zeilennummern von Aufgaben stimmen mit der Datei überein;
  Wiki-Links mit Anker und Alias.
- **Section-Edits** — alle acht Modi; Section bis zur nächsten Überschrift
  gleicher oder höherer Ebene; Unterabschnitte gehören dazu; die drei
  Fundstufen exakt / ohne Groß-Klein / Präfix; Zusammenfallen von Leerzeilen.
- **`StringEdit`** — nicht gefunden, mehrfach gefunden ohne `replace_all`,
  leeres `old_string`, identische Argumente.
- **Optimistisches Sperren** — falscher Hash wird abgelehnt, richtiger
  akzeptiert, fehlende Datei mit gesetztem Hash ergibt „nicht gefunden".
- **Papierkorb und atomares Schreiben** — nach einem Überschreiben liegt der
  alte Inhalt im Papierkorb; keine `.sbtmp-`-Reste.
- **Index** — Reconcile erkennt neue, geänderte und entfernte Dateien;
  Linkauflösung in allen fünf Stufen; Mehrdeutigkeit lässt einen Link
  gebrochen; FTS-Treffer und BM25-Reihenfolge.
- **Layouts** — jedes Layout legt seine Verzeichnisse und seine
  `instructions.md` an.
- **OAuth** — PKCE-Fehlschlag, Wiedereinlösung eines Codes, Rotation des
  Refresh Tokens, Wiederverwendung tötet die Familie, `redirect_uri` passt
  nicht, unbekannter Client.
- **Leak-Test** — die wichtigste Prüfung: über eine breite Matrix von
  Aufrufen darf kein Notizinhalt in einer Logzeile auftauchen, und kein
  Passwort in irgendeiner Ausgabe.

`test/e2e.py` prüft gegen einen laufenden Server: `/healthz`, die
`401`-Antwort mit `resource_metadata`, beide Discovery-Dokumente, die
dynamische Registrierung, den vollständigen PKCE-Ablauf und einen Durchgang
über die Werkzeugoberfläche.

## 21. Grenzen und bekannte Schwächen

Ehrlich benannt, in der Reihenfolge, in der sie jemandem begegnen werden.

1. **`vault_grep` nimmt `include_content` entgegen und wertet es nicht aus.**
   Das Argument steht im Schema, hat aber in v1 keine Wirkung; die
   Trefferzeile wird immer geliefert. Es ist dokumentiert statt entfernt,
   damit ein Client, der es setzt, nicht an `additionalProperties: false`
   scheitert.

2. **Der Papierkorb hat eine Sekundenauflösung.** Zwei Schreibvorgänge auf
   dieselbe Notiz innerhalb derselben Sekunde erzeugen denselben Dateinamen;
   die zweite Kopie überschreibt die erste. Mit aktiver Versionierung ist der
   Stand trotzdem im Commit erhalten.

3. **`attachment_put` kennt weder Papierkorb noch `dry_run`.** Mit
   `overwrite: true` ist der bisherige Inhalt nur über Git wiederherstellbar.
   Binärdateien sind auch der einzige Fall, in dem ein Diff nichts nützt.

4. **`note_move` mit `overwrite: true` legt keine Kopie an.** Der Umzug ist
   ein `os.Rename`; eine am Ziel liegende Notiz wird dabei ersetzt, ohne
   vorher in den Papierkorb zu gehen. Ohne `overwrite` — dem Standard — wird
   der Umzug abgelehnt.

5. **`listChanged: true` ist ein Versprechen ohne Sender.** Der Server
   meldet die Fähigkeit an, sendet aber in v1 keine
   `notifications/tools/list_changed`. Ändert sich durch einen Reload die
   Werkzeugliste eines Users, erfährt der Client es erst beim nächsten
   `tools/list`.

6. **Das Löschen eines Vaults gibt es nicht.** Ein Vault wird über ein
   Werkzeug angelegt, aber nur über das Dateisystem wieder entfernt. Das ist
   Absicht — ein Werkzeug, das ein ganzes Verzeichnis Wissen entfernt, wäre
   das gefährlichste in der Liste.

7. **Der Index behält ein Vault-Verzeichnis, das verschwindet.** Wird ein
   Vault-Verzeichnis im laufenden Betrieb entfernt, bleibt das
   `Vault`-Objekt bestehen, bis der Prozess neu startet.

8. **Keine Auflösung von Konflikten.** Gleichzeitige Änderungen an derselben
   Notiz führen zu einer Ablehnung mit dem aktuellen Hash, nicht zu einer
   Zusammenführung. Wer will, liest neu und schreibt erneut.

9. **Ein Schreib-Mutex je Vault.** Zwei Schreibvorgänge in demselben Vault
   werden serialisiert, auch wenn sie verschiedene Dateien betreffen. Bei der
   erwarteten Last ist das unbemerkt; bei einem Massenimport ist es die
   Grenze.

10. **Der Index sitzt auf einer einzigen SQLite-Verbindung.** Suchen laufen
    nicht parallel. Für einige zehntausend Notizen ist das richtig
    dimensioniert; für Größenordnungen darüber ist es das nicht.

11. **Tokens leben nur im Speicher.** Ein Neustart meldet jeden Client ab.
    Das ist bei aegis eine Sicherheitseigenschaft und hier eher eine
    Unbequemlichkeit — übernommen, weil ein Token-Speicher auf der Platte im
    selben Volume läge wie die Notizen.

12. **Kein `Origin`-Schutz in der Voreinstellung.** `allowed_origins` ist leer,
    weil gehostete MCP-Clients sonst nicht funktionieren. `/mcp` ist
    Bearer-geschützt; wer ausschließlich eigene Clients betreibt, sollte die
    Liste füllen.

13. **Keine Verschlüsselung im Ruhezustand und keine Rechte unterhalb eines
    Vaults.** Die Notizen liegen im Klartext, und die feinste Grenze ist der
    Vault. Wer einen Vault lesen darf, liest ihn ganz.

14. **`vault_replace` bricht mitten im Lauf ab, wenn `limit` überschritten
    wird.** Im Probelauf ist das folgenlos. Mit `dry_run: false` sind die
    Notizen vor dem Abbruch bereits geändert — der Abbruch ist eine Bremse,
    keine Transaktion.

15. **`git_token` kennt keine `env:`- oder `file:`-Auflösung.** Nur Passwörter
    werden aufgelöst. Ein Token gehört deshalb in
    `SECONDBRAIN_GIT_TOKEN`, nicht in eine Konfigurationsdatei.

16. **Prompt Injection aus Notizinhalten ist nicht adressiert.** Eine Notiz,
    die eine Anweisung enthält, wird ausgeliefert wie jede andere. Die
    Gegenmaßnahme ist hier nicht Filterung, sondern Nachvollziehbarkeit: jede
    Änderung steht im Audit-Log, im Diff und im Commit.

17. **Die Metriken kennen keine Histogramme.** Laufzeiten gibt es nur als
    Summe je Werkzeug; Perzentile lassen sich daraus nicht bilden. Die Zähler
    liegen außerdem allein im Speicher und beginnen nach einem Neustart bei
    null. Beides ist der Preis dafür, dass keine Prometheus-Client-Bibliothek
    eingebunden ist (§16.9).
