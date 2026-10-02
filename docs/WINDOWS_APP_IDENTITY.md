# Windows application identity

Reasonix Desktop uses the version-independent AppUserModelID
`io.reasonix.desktop` for its Electron windows, launcher, Go desktop process,
shortcuts, and notifications. Its visible notification name remains `Reasonix`.
This identity is separate from Reasonix Studio (`io.reasonix.studio`) and the
older Tauri desktop (`dev.reasonix.desktop`). Wails Studio v2.10.0 and older
Reasonix Desktop builds used the shared ID `Reasonix`; new Desktop builds no
longer claim it.

## Installation and upgrade

Use `Reasonix.exe` at the installation root to open Desktop; `reasonix-cli.exe`
is the separate command-line entry. New installations and ZIPs extracted into
an empty directory contain only that GUI entry. Existing installations retain
`reasonix-launcher.exe` when present, with no scheduled removal, so old pins,
custom shortcuts and scripts continue working. Managed upgrades update both
names from the same verified launcher bytes. Files inside `versions/`, including
the Electron `app/Reasonix.exe`, remain internal components.

Before showing any Electron window, Desktop sets its relaunch command and icon
to `Reasonix.exe`, falling back to the legacy launcher when necessary. New taskbar pins therefore
do not depend on the versioned Electron executable or its service-path environment.

The installer applies the identity to the exact shortcuts it creates before
launching Desktop. It invokes the installed launcher with the maintenance
command `--repair-shortcuts <absolute.lnk...>`, which repairs existing, owned
links and exits without starting a window, a service, or a legacy migrator.

Normal launcher and desktop startup also repair existing Reasonix-named links
in the installation directory, private/public desktop and Start Menu Programs
directories (including their Reasonix subdirectory), and the current user's
taskbar pin directory. A filename alone never proves
ownership: the resolved target must be a recognized entry inside this
installation. Directory junctions pointing outside it are not accepted.

An owned shortcut with an empty ID or the old `Reasonix` ID adopts the new ID.
An already updated shortcut can still have a stale target or icon repaired.
Explicit Studio, Tauri, and unknown IDs are left unchanged, even if the link
is named Reasonix. Separate installations are not modified.

Links into `versions/<version>/reasonix-desktop.exe` or
`versions/<version>/app/Reasonix.exe`, as well as a flat `app/Reasonix.exe`, move to the permanent
`Reasonix.exe` when it exists; owned links to `reasonix-launcher.exe` also move
to this canonical entry. With only the legacy launcher available, repair keeps
using that valid entry. The obsolete version can then be
removed without breaking that shortcut. The repair preserves launch arguments,
descriptions, window state, custom icons and custom working directories. Only
empty working directories or the migrated version's executable directory are
normalized to the installation root. The installer repairs existing links
without recreating them. An active flat Go installation keeps
its live Go entry point.

Unreadable or unwritable links are left for a later launch to retry and produce
a warning. Repair never switches the process back to the shared old ID. Windows
Explorer may retain cached pins; if a repaired link still appears separately,
unpin it, start Desktop through its permanent launcher, and pin it again.

## Coexistence and rollback

Signed update payloads and `/REASONIXSTAGE=1` still contain
`reasonix-launcher.exe`: already installed update helpers require this name.
The installed layout is selected separately. An old helper can upgrade directly
and may publish both entry names. A new helper preserves an existing legacy entry
but does not create one in a canonical-only installation. `current.json` schema 1,
`versioned-v1` and the signed payload schema are unchanged.

Extract portable ZIPs into a new directory, or use in-app updates. Manually
overlaying a ZIP leaves old files in place and does not provide transactional
installation; a remaining legacy launcher can still have its previous bytes
until the next managed update. Do not delete it to save space if shortcuts or
scripts may still reference it. A damaged `current.json` requires repair with
a complete installer; do not point shortcuts at a retained old version.

Upgrades deliver the launcher, Electron shell, and Go binaries together through
the existing release-unit mechanism. A rollback must restore the complete old
release. An old launcher or desktop can restore the old shortcut ID; a later
complete upgrade repairs owned links again. Mixing binaries from different
releases is not an identity compatibility guarantee.

The old `Reasonix` notification registration and notification history are not
deleted or migrated: an installed Studio version may still own them. New
Desktop notifications use their own registration. Windows notification
preferences associated with the old identity are not copied to the new one.

Studio's own Electron runtime and notification identity alignment is a separate
follow-up. Desktop does not change Studio files, upgrade Studio, or uninstall it.

## Verification before merging

Run the Windows-native application identity, launcher, and notification tests,
Electron type checking and tests, and the installer packaging checks. Source
and mock tests do not establish Explorer's final grouping behavior.

On Windows 11, test the candidate alongside both Studio v2.10.0 and the current
Studio release. Use separate test installations and data homes. Check fresh
installation, upgrade of existing pins, both launch orders, pin/unpin, restart
from each pin, minimize/restore, independent notification attribution, and
launch after deleting the obsolete Desktop version. Each product must keep
its own taskbar group and launch the correct application. Record the tested
builds and whether installer and portable distributions were exercised.
