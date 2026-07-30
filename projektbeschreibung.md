# secondbrain — Projektbeschreibung

*Was das ist, für wen es gebaut ist, und warum es so aussieht, wie es
aussieht.*

---

## 1. Das Problem

Ein Assistent, der beim Denken helfen soll, braucht Zugriff auf das, was man
schon gedacht hat. Genau daran scheitern die drei naheliegenden Wege.

**Notizen in einer proprietären App.** Notion, Evernote, Apple Notes: der
Text liegt in einer Datenbank, auf die man nur durch genau ein Programm kommt.
Solange dieses Programm existiert, gepflegt wird und eine API hat, ist alles
gut. Wenn nicht, ist ein Jahrzehnt Denkarbeit ein Exportformat, das niemand
wieder importiert. Eine Wissensbasis, die man ohne ihre Software nicht lesen
kann, ist keine Anlage, sondern eine Geisel.

**Ein Assistent ohne Gedächtnis.** Der Kontext eines Modells endet mit der
Unterhaltung. Man erklärt dieselbe Projektlage zum vierten Mal, weil die
ersten drei Erklärungen nirgends geblieben sind. Was fehlt, ist kein größeres
Kontextfenster, sondern ein Ort, an dem Erkenntnisse liegen bleiben und
wiedergefunden werden — und zwar von Mensch und Modell gleichermaßen.

**Ein Vault per SSH oder Dateizugriff freigeben.** Der pragmatische Weg: der
Agent bekommt eine Shell oder ein Filesystem-Tool und darf im Obsidian-Vault
arbeiten. Das funktioniert, bis es das erste Mal nicht funktioniert. Ein
`cat` gefolgt von einem `write` ersetzt eine Datei vollständig, und in der
neuen Fassung fehlt der Absatz, den das Modell für unwichtig hielt. Es gibt
keinen Index, also wird nicht gesucht, sondern neu geschrieben — und dieselbe
Idee liegt nach zwei Wochen unter drei Namen in drei Verzeichnissen. Es gibt
keine Historie außer der, die man selbst angelegt hat, und keine Grenze
zwischen `wiki/` und `.ssh/`.

Der unangenehme Teil an Punkt drei: **ein Agent, der Prosa bearbeitet,
scheitert anders als einer, der Code bearbeitet.** Falscher Code kompiliert
nicht, ein Test schlägt fehl, jemand merkt es innerhalb von Minuten. Ein
falscher Eingriff in eine Notiz ist ein Absatz, der verschwindet, und die
Rückmeldung kommt Monate später, wenn man ihn sucht. Alles Weitere in diesem
Projekt folgt aus dieser Asymmetrie.

## 2. Die Idee

secondbrain ist ein MCP-Server über einem Verzeichnis Markdown-Dateien. Er
gibt einem Agenten genau die Operationen, die man beim Arbeiten mit Notizen
tatsächlich braucht — suchen, gliedern, einen Abschnitt ergänzen, verschieben,
zusammenführen —, und macht die gefährlichen davon rückgängig machbar.

Die Notizen bleiben, was sie waren: `.md`-Dateien mit optionalem
YAML-Frontmatter, in Verzeichnissen, die man in Obsidian öffnen, mit `grep`
durchsuchen und mit `git` versionieren kann. Alles, was der Server für sich
selbst braucht — Suchindex, Papierkorb, Konventionsdatei —, liegt in
`<vault>/.secondbrain/` und ist wegwerfbar. Löscht man den Container, bleibt
ein Verzeichnis Text zurück, das ohne dieses Programm vollständig nutzbar
ist.

Diese Software ist eine Linse auf die Notizen, kein Behälter für sie.

## 3. Wie eine Wissensbasis kaputtgeht

Nicht durch ein Ereignis, sondern durch fünf Vorgänge, die alle leise sind.

| Zerfallsmuster | Antwort im Entwurf |
| --- | --- |
| Ein Absatz verschwindet beim Umschreiben | `note_section_edit` und `note_edit` ändern einen Ausschnitt, nicht die Datei; `dry_run` liefert vorher den Diff |
| Zwei Programme schreiben nacheinander in dieselbe Datei | `content_hash`: bei `note_write` Pflicht, sonst optional. Ein veränderter Stand wird nicht überschrieben, sondern abgelehnt |
| Dieselbe Idee entsteht dreimal unter drei Namen | Suche ist billiger als Schreiben: `note_search`, `note_related`, und `note_create` scheitert an einem vorhandenen Pfad |
| Eine Löschung ist endgültig | Nichts wird ersatzlos entfernt: zeitgestempelte Kopie im Papierkorb, dazu ein Commit |
| Der Vault verrottet ungesehen | `vault_review` benennt Stubs, verwaiste Notizen, tote Links, alte Notizen und offene Aufgaben |

Keine dieser Maßnahmen macht einen Agenten sorgfältig. Sie machen
Unsorgfalt reparierbar, und das ist der erreichbare Teil.

## 4. Ziele und Nicht-Ziele

**Ziele**

1. Die Notizen bleiben ohne diese Software vollständig les- und benutzbar.
2. Keine Änderung durch ein Werkzeug ist unwiederbringlich.
3. Ein Werkzeug, das mehr als eine Datei anfasst, zeigt vorher, was es täte.
4. Fremde Schreibzugriffe — Obsidian, `git pull`, `rsync` — sind der
   Normalfall, nicht die Störung.
5. Ein Vault bringt seine eigenen Konventionen mit und erklärt sie dem Modell
   selbst.
6. Mit einem Standard-MCP-Client ohne Sonderbehandlung nutzbar: URL einfügen,
   anmelden.

**Nicht-Ziele**

- Kein Notizprogramm. Es gibt keine Oberfläche zum Lesen und Schreiben; dafür
  gibt es Obsidian und jeden Texteditor.
- Kein Synchronisationsdienst. Die Synchronisation ist `git`, Syncthing, ein
  Netzlaufwerk oder was auch immer schon läuft.
- Keine Multi-Tenant-Anwendung. Eine Person oder ein kleines Team, eine
  Instanz.
- Kein Agent. secondbrain entscheidet nichts über Inhalte; es stellt
  Operationen bereit und protokolliert sie.
- Kein Ersatz für ein Aufgabensystem. Checkboxen in Notizen sind auffindbar,
  mehr nicht.

## 5. Architektur

```
                         ┌────────────────────────────────────────┐
  MCP-Client             │            secondbrain :2020           │
  (Claude, Agent, …)     │                                        │
        │                │  OAuth-2.1-Server                      │
        ├─ Discovery ───►│   /.well-known/*   /register           │
        ├─ Login ───────►│   /authorize (Anmeldeseite)            │
        ├─ Token ───────►│   /token (PKCE S256)                   │
        │                │            │                           │
        │                │      Identität = User                  │
        │                │            │                           │
        └─ MCP ─────────►│  /mcp ──► 34 Tools                     │
                         │            │                           │
                         │   ┌────────▼─────────┐                 │
                         │   │ Pfad prüfen      │                 │
                         │   │ content_hash     │                 │
                         │   │ transformieren   │                 │
                         │   │ Diff erzeugen    │                 │
                         │   │ Papierkorb       │                 │
                         │   │ atomar schreiben │──────────┐      │
                         │   │ Index, Commit    │          │      │
                         │   │ Audit → stdout   │          │      │
                         │   └──────────────────┘          ▼      │
                         │                        /data/<vault>/  │
                         │   fsnotify ◄───────────  *.md          │
                         │   SQLite FTS5            .secondbrain/ │
                         │                          .git/         │
                         └────────────────────────────────────────┘
                                          ▲
                                Obsidian, git pull, rsync
```

Vier Schichten mit je einer Aufgabe:

1. **Identität** — OAuth 2.1 stellt fest, *wer* fragt. Daran hängen die
   erlaubten Vaults und, bei `read_only`, die überhaupt angebotene
   Werkzeugliste.
2. **Werkzeuge** — 34 Operationen mit je einem klaren Zweck.
3. **Schreibpfad** — jede Mutation läuft durch dieselbe Funktion, die prüft,
   transformiert, diffed, sichert, atomar schreibt, indiziert und committet.
4. **Index** — SQLite mit FTS5, gegen das Dateisystem abgeglichen und von
   einem fsnotify-Watcher aktuell gehalten. Nichts darin ist maßgeblich.

## 6. Entscheidungen

Jede Entscheidung und was sie gekostet hat.

### Markdown-Dateien statt einer Datenbank

Der Preis ist Arbeit: Parsen von Frontmatter, Auflösen von Links, ein
eigener Index, ein Watcher für fremde Änderungen. Eine Datenbank hätte all
das geschenkt.

Bezahlt wird sie mit der einzigen Eigenschaft, die über zehn Jahre zählt:
Die Notizen überleben dieses Programm. Sie lassen sich in Obsidian öffnen,
in einer Pipeline durchsuchen und in ein Repository committen, und ein
Umzug auf eine andere Software ist ein `mv`. Für eine private Wissensbasis
ist das kein Komfortmerkmal, sondern die Existenzberechtigung.

### Go, ein Prozess, keine Fremdinfrastruktur

Ein statisches Binary, ein Container in Megabyte, keine Laufzeitumgebung
darunter. Sechs direkte Abhängigkeiten, und die wichtigste davon —
`modernc.org/sqlite` — ist ein SQLite in reinem Go, weshalb `CGO_ENABLED=0`
gilt und das Laufzeit-Image distroless bleiben kann. Für Git wird `go-git`
benutzt statt eines Aufrufs von `git`, damit im Container kein Git-Binary
liegen muss.

### 34 Werkzeuge statt sechs

Die naheliegende Alternative war ein `note_edit` mit einem `mode`-Parameter
und je nach Modus anderen Argumenten. Sie wurde verworfen, weil ein Modell
Werkzeuge über Name und Beschreibung auswählt: eng geschnittene Werkzeuge
werden richtig gewählt, ein Sammelwerkzeug mit Bedingungslogik wird falsch
bedient — und der Fehler zeigt sich nicht als Fehlermeldung, sondern als
beschädigte Notiz.

Die Kosten sind reale: eine lange Werkzeugliste in jedem Kontextfenster.
Dafür wurden die Beschreibungen so geschrieben, dass sie sagen, *wann* ein
Werkzeug das falsche ist — `vault_grep` verweist auf `note_search`,
`note_write` auf `note_edit`.

### `content_hash` statt Sperren

Ein Vault wird von mehreren Seiten beschrieben. Eine Sperre über eine Datei,
die auch Obsidian gerade offen hat, wäre eine Lüge. Stattdessen gibt jeder
Lesevorgang einen Hash zurück, und `note_write` verlangt ihn. Hat sich die
Datei seither geändert, wird der Schreibvorgang abgelehnt, nicht
zusammengeführt: Zusammenführen wäre Raten, und geraten wird hier nicht.

### Ein Index, der jederzeit wegwerfbar ist

`<vault>/.secondbrain/index.db` ist ein Cache. Er wird beim Start gegen das
Dateisystem abgeglichen, danach von einem fsnotify-Watcher mit 400 ms
Entprellung nachgeführt, und wenn er beschädigt ist, wird er kommentarlos
gelöscht und neu gebaut. Damit ist ein externer Editor ein normaler
Vorgang statt einer Gefahrenquelle — und das ist die Voraussetzung dafür,
dass man den Vault überhaupt noch selbst bearbeiten mag.

### Papierkorb *und* Git

Zwei Netze, weil sie an verschiedenen Stellen reißen. Git kann abgeschaltet
sein oder für ein Repository fehlschlagen; der Papierkorb ist eine
Dateikopie und funktioniert immer. Umgekehrt beantwortet nur Git die Frage,
*was* sich zwischen zwei Ständen geändert hat, und trägt im Commit-Text den
Namen des Werkzeugs, das es getan hat.

### Konventionen im Vault, nicht im System-Prompt

Jedes Layout schreibt eine `instructions.md`, deren Inhalt in der
`initialize`-Antwort an den Client geht. Die Regeln eines Vaults reisen
damit mit dem Vault, statt in der Konfiguration eines Clients zu liegen, den
man vielleicht gar nicht mehr benutzt. Sie zu ändern heißt, eine Datei in
den Notizen zu bearbeiten.

### Layout `wiki-raw` als Standard

Die Trennung, die sich in der Praxis am deutlichsten auszahlt, ist die
zwischen Quellmaterial und Verstandenem. `raw/` wird nie umgeschrieben —
korrigiert man dort einen Tippfehler, hat man das Beweisstück verändert.
`wiki/` wird ständig umgeschrieben. Liegt beides im selben Verzeichnis,
verschwimmt die Grenze innerhalb weniger Wochen.

### Metriken aus, und von Hand geschrieben

Der Prometheus-Endpunkt ist abgeschaltet, bis jemand ihn einschaltet. Eine
Wissensbasis ist kein öffentlicher Dienst, und die Zahlen, die dort stehen —
wie viele Notizen jemand hat, wie viel er schreibt, wie viele Aufgaben offen
sind — sind aussagekräftig, auch wenn kein einziger Notiztext mitgeliefert
wird. Wer sie braucht, schaltet sie ein und schützt sie mit einem Schlüssel
oder, wirksamer, mit einem zweiten Listener auf einem Port, den niemand
veröffentlicht: ein nie gemappter Port ist von außen nicht erreichbar,
gleichgültig wie gut der Schlüssel ist. Die Exposition selbst ist
handgeschrieben, ohne Client-Bibliothek — das Textformat sind zwei Dutzend
Zeilen, und einen Abhängigkeitsbaum dafür in einen Container zu ziehen, in
dem die privaten Notizen von jemandem liegen, ist dasselbe schlechte Geschäft
wie überall sonst hier. Gekostet hat es die Histogramme: Dauern gibt es nur
als aufsummierte Sekunden je Werkzeug, also als Mittelwert über `rate()`, und
Perzentile gibt es nicht.

## 7. Entscheidungen, die bewusst *nicht* getroffen wurden

### Kein Web-UI

Es gibt keine Oberfläche außer der Anmeldeseite. Ein Editor im Browser wäre
ein zweites, schlechteres Obsidian, und er wäre Angriffsfläche in einem
Prozess, der auf einem Verzeichnis mit privaten Aufzeichnungen schreibend
sitzt. Die Oberfläche ist der Editor, den man ohnehin benutzt; die
Konfiguration ist eine Datei.

### Keine Embeddings in v1

Semantische Suche wäre das erste, was man einbauen möchte, und genau deshalb
ist sie draußen geblieben: Sie zieht ein Modell, einen API-Schlüssel oder
eine lokale Inferenz nach sich, verlangt Neuberechnung bei jeder Änderung und
kostet bei jeder Anfrage Geld oder Zeit. In einem Vault mit gepflegten Links
liefert `note_related` — seltene Tags stärker gewichtet, Links in beide
Richtungen, Ko-Zitation — einen großen Teil desselben Nutzens für nichts.

Die Tabelle `embeddings` existiert trotzdem schon im Schema. Wenn semantische
Suche später kommt, ist das eine Datenmigration und keine Schemamigration,
und bestehende Indizes bleiben lesbar.

### Kein erzwungenes Schema

Es gibt keine Pflichtfelder im Frontmatter, keine Validierung von Tags, keine
Regel, die einen Pfad verbietet, weil er nicht ins Layout passt. Ein Layout
ist eine Meinung in einer Textdatei, keine Zwangsjacke. Der Grund ist
schlicht: Die Dateien gehören dem Menschen. Eine Software, die eine
handgeschriebene Notiz zurückweist, weil `tags:` fehlt, wird umgangen — und
umgangene Software schützt niemanden. Aus demselben Grund pflegt secondbrain
`created` und `updated` nur in Notizen, die bereits Frontmatter haben oder
gerade angelegt werden.

### Kein hartes Löschen

Kein Werkzeug entfernt einen Inhalt ersatzlos. `note_delete` legt eine
zeitgestempelte Kopie in `<vault>/.secondbrain/trash/`, entfernt die Notiz
dann von ihrem Pfad und meldet, welche Notizen jetzt ins Leere zeigen. Auch
ein Überschreiben legt vorher eine Kopie ab. Erst die Aufbewahrungsfrist
(Standard 30 Tage) räumt auf.

Der Preis ist Plattenplatz für Text, also praktisch keiner. Der Gegenwert
ist, dass die gefährlichste Frage — „was, wenn der Agent Unsinn baut?" — eine
Antwort hat, die nicht „Backup einspielen" lautet.

### Kein `read_only`-Container

aegis läuft mit einem schreibgeschützten Root-Dateisystem, secondbrain kann
das nicht: Es schreibt die Notizen, den Index und ein Git-Repository pro
Vault. Statt die Eigenschaft vorzutäuschen, wird sie benannt. Was bleibt,
ist `cap_drop: ALL`, `no-new-privileges` und ein Nicht-Root-Benutzer — und
die Folge daraus, die man kennen muss: Der distroless-`nonroot`-Benutzer ist
UID 65532, und ein eingebundenes `/data` muss ihm gehören.

### Kein Ratenlimit pro Werkzeug, keine Freigabeschleife

Es gibt ein grobes Limit pro Benutzer, aber keine Bestätigung durch einen
Menschen vor einem Schreibvorgang. Eine Rückfrage bei jeder Änderung wäre
nach drei Tagen ein Reflex, den niemand mehr liest, und damit keine
Sicherung. Die Sicherung ist stattdessen, dass jede Änderung sichtbar
(`dry_run`, Diff), protokolliert (Audit-Zeile) und umkehrbar (Papierkorb,
Commit) ist.

## 8. Für wen das gedacht ist

Für jemanden, der seine Notizen schon als Dateien führt oder dorthin will,
einen Agenten regelmäßig benutzt und es leid ist, ihm jedes Mal denselben
Zusammenhang zu erklären. Konkret:

- **Persönliche Wissensbasis.** Ein Obsidian-Vault auf einem kleinen Server,
  ein Container davor. Der Assistent liest die Projektnotiz, bevor er
  antwortet, und schreibt zurück, was besprochen wurde.
- **Journal und Rückblick.** `daily_note` beim Erzählen, `daily_range` beim
  Wochenrückblick — sieben Notizen in einem Aufruf statt sieben Runden.
- **Recherche mit Quellentrennung.** Transkripte und Auszüge nach `raw/`,
  Verstandenes nach `wiki/`, verlinkt in beide Richtungen.
- **Ein Team mit einem gemeinsamen Vault.** Jede Person meldet sich als sie
  selbst an, das Audit-Log zeigt, wer was angefasst hat, und ein
  Recherche-Client bekommt einen `read_only`-Benutzer.
- **Ein Vault pro Bereich.** Beruflich und privat getrennt, ein Benutzer je
  Bereich, `vaults:` in der Konfiguration als Grenze.

Der typische Betrieb ist ein Container hinter Traefik oder einem
Cloudflare Tunnel, ein Volume unter `/data`, `SECONDBRAIN_GIT=true` und
optional ein `git_remote`, das jeden Commit in ein privates Repository
schiebt. Damit ist die Sicherung erledigt, ohne dass jemand ein Backup
einrichten musste.

## 9. Was diese Software nicht ist

- **Kein Schutz vor einem Agenten, dem man zu viel erlaubt.** Wer einem
  Client einen Schreibbenutzer gibt, hat einem Modell Schreibrechte auf seine
  Notizen gegeben. secondbrain begrenzt den Schaden und macht ihn
  rückgängig machbar; es liest keine Absichten. Für Clients, die nur lesen
  sollen, gibt es `read_only`.
- **Kein Schutz vor Prompt Injection.** Eine Notiz, die eine Anweisung
  enthält, wird an das Modell ausgeliefert wie jede andere. Das ist die Natur
  einer Wissensbasis, die auch fremde Texte aufnimmt. Was hilft, ist die
  Nachvollziehbarkeit jeder Änderung, nicht deren Verhinderung.
- **Keine Zugriffskontrolle unterhalb des Vaults.** Die feinste Grenze ist
  ein Vault. Es gibt keine Rechte pro Verzeichnis oder pro Notiz — wer einen
  Vault lesen darf, liest ihn ganz.
- **Kein Konfliktlöser.** Zwei gleichzeitige Änderungen an derselben Notiz
  führen zu einer Ablehnung, nicht zu einem Merge. Das ist Absicht: ein
  automatisch zusammengeführter Absatz ist genau die Art Schaden, die dieses
  Projekt vermeiden soll.
- **Keine Verschlüsselung im Ruhezustand.** Die Notizen liegen als Klartext
  im Volume. Wer das nicht möchte, verschlüsselt das darunterliegende
  Dateisystem.
- **Nicht auf riesige Vaults ausgelegt.** Der Index kommt mit einigen
  zehntausend Notizen zurecht. Eine Million Dateien ist ein anderes Problem
  mit anderen Werkzeugen.

## 10. Ausblick

**v1** ist alles, was hier beschrieben ist: 34 Werkzeuge, FTS5-Suche,
fsnotify-Abgleich, Papierkorb, Git-Versionierung, vier Layouts,
OAuth 2.1 mit DCR und PKCE, Audit-Log, Docker-Image.

**Später, wenn es sich verdient**

- Semantische Suche über die bereits vorhandene `embeddings`-Tabelle
- Ein Wartungslauf nach Zeitplan statt nur auf Abruf
- Export eines Vaults als einzelnes Archiv über ein Werkzeug
- Anhänge in mehr Formaten, weiterhin über eine Positivliste
- Auslieferung des Audit-Logs an einen Webhook

**Ausdrücklich abgelehnt** — ein Web-Editor, ein Plugin-System, ein
erzwungenes Frontmatter-Schema, automatische Zusammenführung bei Konflikten.
Jedes davon würde entweder Angriffsfläche hinzufügen oder die eine
Eigenschaft beschädigen, die dieses Projekt trägt: dass am Ende ein
Verzeichnis gewöhnlicher Textdateien dasteht.
