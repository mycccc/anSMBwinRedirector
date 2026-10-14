# anSMBwinRedirector

A small Windows utility that performs **transparent TCP destination-port rewriting**, so that the
native Windows SMB client / File Explorer can reach an `anSMBserver` instance running on Android
that listens on a **non-privileged port** (default `4450`, configurable).

```text
Windows Explorer / native SMB client
        │  TCP: <ephemeral port> → AndroidIP:445
        ▼
anSMBwinRedirector
        │  transparently rewrites the DESTINATION PORT: AndroidIP:445 → AndroidIP:<Android SMB port>
        ▼
Android anSMBserver (actually listening on that configurable port)
        │  TCP: AndroidIP:<port> → Windows ephemeral port
        ▼
anSMBwinRedirector
        │  transparently restores the SOURCE PORT: <port> → 445
        ▼
Windows Explorer / native SMB client
```

**Why this exists**: Android apps cannot bind privileged ports below 1024, so `anSMBserver` cannot
listen on `445`; the native Windows SMB client only talks to `445`, and UNC paths offer no way to
specify a port. (Note the direction of causation: `anSMBserver` using a non-standard port is a
consequence of an **Android platform limitation**, **not** a way to avoid clashing with
Windows' own `445`.)

> Chinese documentation: [`README.zh-CN.md`](README.zh-CN.md).

---

## 1. Requirements

| Item | Requirement |
| --- | --- |
| OS | **Windows x64 (amd64)** — nothing else |
| Privileges | **Administrator** (needed to load the WinDivert driver) |
| Same-directory files | **`WinDivert.dll` and `WinDivert64.sys` must sit next to the exe**; both are required (see §4) |
| Working directory | **The directory containing the exe must be writable** (the config, its backup and the temp file are written there — see §6 / §7) |
| Network | Must be able to reach the target Android device's IP |

> ⛔ **Windows on ARM64 is not supported**: an amd64 binary may run under emulation, but a kernel
> driver must be ARM64, so the bundled amd64 `WinDivert64.sys` cannot load. The program **refuses to
> start** with an explicit explanation before doing anything else.
> ⛔ 32-bit Windows is not supported (an amd64 executable will not start).

---

## 2. Installation and layout

**Unzip and run — there is no installer.** Extract the release package into any **writable** directory:

```text
anSMBwinRedirector-<version>-win-x64/
├── anSMBwinRedirector.exe
├── WinDivert.dll            ← third-party (WinDivert 2.2.2-A); must sit next to the exe
├── WinDivert64.sys          ← third-party; must sit next to the exe
├── language.json            ← UI strings (`en` + `zh-CN`; UTF-8)
├── LICENSE                  ← full GPLv3 text for this project
├── THIRD_PARTY_NOTICES.md   ← third-party components and their full licenses
└── README.md                ← this file
```

- The release package contains **exactly these 7 files** and **no `config.json`** (that is runtime
  data, created on your first "Apply Changes" — see §3/§6).
- The Chinese documentation `README.zh-CN.md` lives **only in the source repository** and is not shipped
  in the release package (the package is fixed at the 7 files listed above).
- The program writes **nothing outside its own directory**, and **never writes `%PROGRAMDATA%`**.
- ⚠️ **Do not put it in a directory you cannot write to** (e.g. `C:\Program Files\...` with default
  permissions): the first save would fail, and the program treats "config directory not writable" as
  **Config Unavailable** (see §7).
- The directory is **self-contained**: if you move or rename it, the configuration and the WinDivert
  files move with it (see the upgrade notes in §5).

---

## 3. First run

1. Right-click `anSMBwinRedirector.exe` → **Run as administrator**.
2. The main window appears (along with a tray icon). **No configuration file is created yet** —
   the first start is an empty configuration; `config.json` appears in the **exe's directory** only
   when you first press "Apply Changes".
3. Press "Add" and fill in a target:
   - **Android IP**: the device's IPv4 address (e.g. `192.168.1.100`);
   - **Android SMB port**: the port `anSMBserver` actually listens on (default `4450`, configurable);
   - **username / password** (optional): if entered, they are written to the Windows credential store
     (`cmdkey`) before connecting. If left empty, existing system credentials are left untouched.
     *Username and password must either both be filled in or both be empty.*
   - *One Android IP can have only one rule.*
4. Press "Apply Changes" → `config.json` is created **next to the exe** (the previous version is kept
   as `config.json.bak`).
5. Press "Start".
6. To open that server's shares: **select the row and press "Connect"** (or right-click the row →
   Connect). The program first writes the credentials (if configured) and then opens `\\<Android IP>\`
   through the system default handler.

After that you can browse `\\<Android IP>\<share>` in File Explorer — Windows still uses `445`, and the
program rewrites it to `<Android IP>:<your configured port>` behind the scenes.

> The program does not modify the system proxy and does not use `netsh portproxy`.

---

## 4. About WinDivert (**please read**)

The redirect works by intercepting and rewriting packets at the network layer, which relies on the
third-party component **WinDivert 2.2.2-A**.

- It consists of **`WinDivert.dll` + `WinDivert64.sys`**, which **must be placed in the same directory
  as `anSMBwinRedirector.exe`**.
- At startup (and on every "Start") the program verifies the **SHA-256** of both files:
  - **missing** → `RUNTIME_FILE_MISSING` (with the full path);
  - **modified** → `RUNTIME_FILE_CORRUPT` (reporting both the **actual** and the **expected** SHA-256).
  - ⛔ In either case the program **only reports**: it never overwrites, never re-downloads and never
    deletes your files.
- On the first "Start", the WinDivert driver is **installed and loaded on demand** (this creates a
  Windows service and requires administrator rights). The install/load itself is done by WinDivert;
  **the "takeover" (stopping and deleting an existing service) is done by this program** — see §4.1.

### 4.1 This program owns the WinDivert service lifecycle (Start / Stop / Exit)

**On "Start" (takeover)**: if a `WinDivert` service **already exists** (left behind by another tool or
an older version), the program first **stops and deletes** it, waits until it is gone (`sc query`
returns **1060**), and only then lets WinDivert install `WinDivert64.sys` **from this program's own
directory**, verifying that the service's actual `ImagePath` points at this directory.

> ⛔ The program **never reuses** an existing WinDivert service — tests showed that doing so keeps
> loading a `.sys` from the **old path**. If the takeover fails it reports an explicit error
> (`WINDIVERT_TAKEOVER_FAILED`) and **never** falls back to the old service.

| Action | Effect on the WinDivert **service** | Effect on the `WinDivert.dll` / `WinDivert64.sys` **files** |
| --- | --- | --- |
| **Start** | if present ⇒ stop and **delete** it, then install from this program's directory | **not** modified (only reported when missing/modified) |
| **Stop** | stop and **delete** the service (`sc query` back to 1060), then wait until `.sys` is no longer locked | **not** deleted |
| **Exit** | measured: if it was running, after exit `sc query WinDivert` is **1060 (not installed)** | **not** deleted |
| **OS reboot** | ⛔ does **not** delete the service registration | ⛔ does **not** delete those two files |

> ⚠️ **Do not confuse "reboot" with "cleanup"**: a reboot only makes the **currently loaded driver
> instance** go away with the system shutdown (which is why it can resolve driver-version conflicts);
> it neither deletes the service registration nor the two files (they are just ordinary files you
> extracted).

⛔ The program offers **no** "force cleanup / bypass-the-takeover" uninstall entry point and **never
deletes** your `WinDivert.dll` / `WinDivert64.sys`. If you want to clean up a leftover registration
created elsewhere, do it yourself following WinDivert's official instructions (that is not a feature
of this program).

### 4.2 The "Debug" window

The "Debug" button opens a **diagnostics** window with three parts:

1. **Read-only diagnostics** (modifies nothing): the program directory; the paths, **file versions**
   and **actual / expected SHA-256** of `WinDivert.dll` / `WinDivert64.sys`; whether the WinDivert
   service exists, its current state, its `ImagePath` and whether it points at this program's
   directory; the **runtime driver version** (read-only diagnostics **do not open a driver handle**,
   so this item reads "not queried").
2. **`Repair / Reinstall WinDivert`**: removes an existing service registration through the **same
   takeover flow** as §4.1 and re-verifies the two runtime files in this program's directory.
   **It does not load the driver** (the driver is still loaded by the next "Start") and ⛔ does not
   modify any file. The button is disabled while the redirect is running (press "Stop" first).
3. **`Restart the Windows Workstation service`**: its purpose is to **clear the Windows SMB client
   session state**. ⛔ It is **not** a "network repair" and does **not** guarantee a fix (measured).
   It asks for confirmation first and interrupts existing SMB connections.

> ⚠️ Honest statement: WinDivert consumers on the `REFLECT` layer **cannot be enumerated**, so the
> program **cannot prove** that no other WinDivert consumer exists on the system.

---

## 5. Upgrading from an older version (legacy config migration)

Older versions kept the configuration at `%APPDATA%\anSMBwinRedirector\config.json`. **It now lives
next to the exe** (see §6). On upgrade the program performs a **one-time migration**:

| Situation | What the program does |
| --- | --- |
| New location has **no** config, old location has a **usable** one | Read and validate the old config → **safely write** it to the new location → **read it back and verify** → **rename** the old file to `config.json.migrated-<timestamp>` (content fully preserved, ⛔ never deleted) |
| New location **already has** a config | The **new location wins**; the old file is **left untouched** (a note is added to "Recent events") |
| The old config is **unusable** (parse/validation failure) | ⛔ **no migration, no rename**; the old file is left as-is; the program logs a diagnosable note and starts normally with an empty configuration |
| **Any step** of the migration fails | ⛔ the old file stays **byte-for-byte identical**, and the program does **not** report success (the new location may still be an empty config) |

- The migration happens **exactly once**: after the rename there is no `config.json` at the old location.
- ⛔ There is **no "fall back to `%APPDATA%`" path**: after the migration the program **no longer reads
  or writes** `%APPDATA%`.
- Once you are sure you no longer need the old file, you may delete `config.json.migrated-<timestamp>`
  yourself — ⚠️ **it contains plaintext passwords just like `config.json`** (see §6).

---

## 6. Configuration file

```text
<directory containing anSMBwinRedirector.exe>\config.json
```

- Backup and temp files live in the **same directory**: `config.json.bak` (previous valid config) and
  `config.json.tmp` (in-flight write).
- **The first start does not create the file**: it appears only on your first "Apply Changes".
- Saving works by "write a temp file first, then perform one file replacement": the program **never
  deletes the current valid `config.json` first** (⚠️ it does **not** promise disk-level atomicity
  against power loss or crashes).

```json
{
  "schema_version": 2,
  "targets": [
    {
      "id": "<stable UUID>",
      "android_ip": "192.168.1.100",
      "android_port": 4450,
      "username": "ansmb",
      "password": "……"
    }
  ]
}
```

- `id` is generated by the program and stays stable; **`android_ip` must be unique** within the rule
  set — duplicates are rejected.
- Each rule has four configurable fields: **Android IP**, **Android SMB port**, **username**,
  **password** (the last two may be empty; either both or neither). The matching Windows-side **TCP
  445** is fixed and not configurable.
- `targets` may be empty (meaning "no redirect target configured").

> ⚠️ **Known trade-off in v0.x/v1.0: the password in `config.json` is stored in plaintext.**
> The program does **not claim** secure storage — it neither encrypts the file nor puts the
> configuration itself into the Windows credential manager. The file sits next to the exe and
> inherits that directory's access control; please secure the directory and the disk yourself.
> **`config.json.bak` and any leftover `config.json.migrated-<timestamp>` contain plaintext passwords too.**
> (The Windows credentials written by "Connect" are kept by the **system credential manager** and are
> unrelated to these files.)
> ⛔ Passwords **never** appear in logs, diagnostics details, the Debug window, error messages or window titles.

---

## 7. Config Unavailable (and "directory not writable")

**Principle**: any **read / parse / validation** failure, or an **unwritable config directory**, puts
the program into the **Config Unavailable** state. In that state the program:

```text
- loads no targets at all;
- creates no runtime and opens no WinDivert handle;
- disables "Start" (so it can never enter the running state);
- disables "Apply Changes" (to avoid overwriting a repairable file);
- does not fall back to reading the old %APPDATA% configuration;
- does not silently continue with an empty config, and never deletes or rewrites any file;
- shows the file path plus the failure reason, with an entry point such as "Copy config path"
  for manual repair.
```

**Write probe at startup**: on every start the program performs one **lightweight write probe** in the
config directory (= the exe's directory) — it creates a **dedicated, uniquely named** probe file
(filename prefix `.ansmbwinredirector-write-probe-`), writes a few bytes and **deletes it immediately**.

- The probe only cleans up **the file it created itself**; ⛔ it never uses user file names such as
  `config.json`, so it **cannot** leave an empty `config.json` behind.
- If the probe fails (or if **cleaning up** the probe file fails) ⇒ the program goes straight into
  Config Unavailable, reporting the **directory path plus the underlying error**.
- ⚠️ A successful probe does **not** guarantee that saving will succeed: the actual save still checks
  every write / backup / replacement step independently.

**Common causes**

| Symptom | Action |
| --- | --- |
| `CONFIG_IO_ERROR` (directory not writable / insufficient permissions) | Make the program directory writable (or move it to a writable directory); e.g. **avoid** `C:\Program Files\` with default permissions |
| `CONFIG_IO_ERROR` (probe file cannot be cleaned up) | Check whether security software is locking the directory; this is also treated as "not writable" |
| `CONFIG_PARSE_ERROR` / `CONFIG_VALIDATION_FAILED` | Use "Copy config path" to open and repair the file by hand; the UI lists **all** violations |
| `CONFIG_SCHEMA_UNSUPPORTED` (file written by a newer version) | ⛔ The program deliberately does **not** offer "restart with an empty config"; use the newer version instead (to prevent accidental downgrade damage) |

---

## 8. Using the UI

**Main window**

| Control | Description |
| --- | --- |
| Add / Edit / Delete | These only change the **draft**; "Edit" lets you set IP / port / username / password; a deleted row becomes "pending delete" and, while it is selected, the "Delete" button turns into "Undo delete" |
| **Apply Changes** | The **only** commit path: validate → write to disk (next to the exe, keeping a `.bak`) → take effect. The status bar shows `⚠ Unsaved changes` while a draft differs |
| Start / Stop | Start/stop redirection for all targets ("Start" first performs the WinDivert takeover — see §4.1) |
| **Connect** | Opens that target's shares: writes the Windows credentials first (if the target has a username/password) and then opens `\\<Android IP>\` through the system default handler. **Requires that target to be running** (⛔ it will not auto-"Start"). If Windows already has credentials for that IP, it **asks before replacing** them |
| Debug | see §4.2 |
| Copy details / Copy config path | Put diagnostics / the configuration file path on the clipboard |
| Exit | If there are unsaved changes it asks "Save and apply / Discard / Cancel" |

**List interactions**: **single click = select** (does not open the editor); **double click = edit**;
**right click = context menu (Connect / Edit / Delete)**.
The "Last connect" column shows the result of the **most recent "Connect" action** (`Reachable(time)` /
`Not connected yet`); it is **not** an independent network probe.

**Tray**: left-click the icon to show the main window; the right-click menu offers status, start, stop,
apply changes (only when there are unsaved changes), show main window, and exit.
**Closing the main window only hides it to the tray**; it does not exit the program.

---

## 9. Network model (one sentence)

For each target the program does exactly one thing: rewrite the **destination port** `445` to the port
you configured (**the destination IP is unchanged and the Windows source port is untouched**), and
restore the source port in the reverse direction. It is **not** "a local 445 → 4450 port forward", nor
a generic `FROM → TO` NAT; tools such as `netsh interface portproxy` **cannot** replace it.

---

## 10. Troubleshooting

| Symptom | Likely cause / action |
| --- | --- |
| `RUNTIME_FILE_MISSING` / `RUNTIME_FILE_CORRUPT` at startup | `WinDivert.dll` / `WinDivert64.sys` in the same directory are missing or modified: put the correct files back (the program **never** downloads or overwrites them) |
| "Cannot take over the system WinDivert service" (`WINDIVERT_TAKEOVER_FAILED`) | The service cannot be stopped/deleted (possibly in use by another program): retry later, or reboot and try again |
| The status bar shows "Config unavailable" | See §7: repair the config file or make the directory writable; until then the program will not start redirection and will not overwrite the file |
| "My targets disappeared after upgrading" | Check whether `config.json` now exists next to the exe; the old configuration is renamed to `config.json.migrated-<timestamp>` (see §5) |
| "Connect" shows **no feedback for a long time** | When the target address is unreachable, Windows may take a long time to return from opening `\\<IP>\` (measured: >3 minutes). This is **system behaviour**, not a verdict from this program; the buttons stay disabled meanwhile |
| Explorer asks for a username/password | That target has no credentials configured; fill in username/password under "Edit", press "Apply Changes", then use "Connect" to store them in the Windows credential manager |
| Want to fully clean up WinDivert leftovers | See §4.1: the program offers no force-cleanup entry point; follow WinDivert's official instructions |

---

## 11. Known limitations (v1.0)

- **Windows x64 only**; no 32-bit and no ARM64 support (ARM64 is explicitly rejected).
- **The exe's directory must be writable**: the configuration and its backup are written there (§6/§7).
- ⛔ **No** "force cleanup / bypass-the-takeover" WinDivert uninstall entry point (§4.1).
- **No** installer, **no** auto-update, **no** code-signed package.
- It is neither an SMB server nor an SMB client, not a generic port forwarder/proxy, and it does not
  support UDP.
- ⛔ **No** encrypted password storage (plaintext trade-off — §6).
- ⚠️ When the target address is **unreachable**, "Connect" may show **no result for a long time
  (measured: >3 minutes)** — this is how Windows behaves when opening `\\<IP>\`.
- ⛔ **No promise** of disk-level atomicity for config writes in extreme situations (power loss, crash) — §6.

---

## 12. Building from source

Go is required (version in `src/go.mod`). The release package is assembled by a script that verifies
the hash chain and prints it:

```sh
scripts/build-release.sh 1.0          # output under src/dist/release/
```

Key points:

- The package contains **exactly 7 files** (§2); `src/dist/` is ignored by `.gitignore`.
- **The manifest is mandatory**: the GUI toolkit `lxn/walk` needs a Common-Controls v6 manifest;
  without it the window **cannot be created at all**. The manifest is **embedded** as a `.syso`
  resource (`cmd/ansmbwinredirector/rsrc_amd64.syso`), which `go tool rsrc` (version pinned via the
  `tool` directive in `go.mod`) can regenerate **reproducibly**; the release script regenerates it and
  compares it **byte-for-byte**, failing on any difference.
- **The WinDivert SHA-256 values are frozen as compile-time constants**
  (`src/internal/winvert/assets_common.go`). When upgrading WinDivert you must update those constants
  too — `go test ./internal/winvert` enforces this.
- ⚠️ The executable is **not** byte-for-byte reproducible across builds: the script injects the
  **build time** into the exe's version information, so each build has a different hash (the other six
  files are byte-for-byte copies of repository content). To check which commit an exe came from, read
  its **embedded `vcs.revision` / `vcs.modified` and version strings**.

---

## 13. License

- This project: **GNU GPLv3 — specifically `GPL-3.0-only`** (i.e. "GPLv3 only", **without** "or any
  later version"). `LICENSE` is the verbatim GNU GPLv3 **full text** (unmodified, including the
  "How to Apply" section at the end).
- Third-party components shipped in the package and their full licenses: see `THIRD_PARTY_NOTICES.md`
  (**WinDivert** is dual-licensed LGPLv3 / GPLv2; this project chooses **LGPLv3**, which is why its
  `WinDivert.dll` / `WinDivert64.sys` are distributed as **separate files** to keep them replaceable).
