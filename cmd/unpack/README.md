# unpack

Double-click an archive in Thunar and it is unpacked where it is, and the
archive is gone. No archive window, no Extract button.

    unpack archive.zip [more.tar.gz ...]

What lands where follows macOS's Archive Utility:

- **One top-level item** -- a folder, or a single file -- lands beside the
  archive as it is. `release.tar.gz` holding `release-1.2/` gives
  `release-1.2/`.
- **Several** go into a folder named after the archive: `photos.zip`
  holding forty pictures gives `photos/`.
- **Nothing is overwritten.** A name already taken gets a number, as in
  Finder: `photos 2`, `report 2.pdf`.
- **Finder litter** (`__MACOSX/`, `.DS_Store`) is left out.

The archive is removed only once everything is in place. Extraction goes
into a hidden `.unpack-*` directory beside it first, so an archive that is
truncated, corrupt, password-protected or hostile leaves nothing behind but
itself, untouched -- and since there is no window, the reason arrives as a
notification.

## Formats

| Extension | Read by |
| --- | --- |
| `.zip` | Go |
| `.tar`, `.tar.gz`/`.tgz`, `.tar.bz2`/`.tbz2`, `.tar.xz`/`.txz`, `.tar.zst`/`.tzst` | Go |
| `.gz`, `.bz2`, `.xz`, `.zst` of a single file (or of a tar under another name) | Go |
| `.7z`, `.rar` | 7-Zip (`7z`), if installed |

The format comes from the **file name only**. A `.docx`, `.epub`, `.jar` or
`.apk` is a zip inside, but is never taken apart and deleted: unpack refuses
anything whose name does not say archive.

Zip names without the UTF-8 flag -- Windows's "Send to compressed folder" --
are decoded as code page 437, so `café.txt` does not arrive as `caf‚.txt`.
Password-protected archives are refused; open those with File Roller.

## Safety

Every entry is written through an `os.Root` on the scratch directory, which
resolves every name inside that directory: an entry named `../../.bashrc`, an
absolute path, a hard link out, or a file written through a symbolic link
the archive itself planted (`d -> /home/you`, then `d/.bashrc`) all fail,
and the archive is refused whole. setuid and setgid bits are dropped;
everything extracted is readable and writable by its owner.

## Installing

**1. Build and install the binary:**

    make unpack
    install -m755 bin/unpack ~/.local/bin/unpack

**2. Register it for archives,** hidden from menus:

    TYPES="application/zip application/x-tar application/x-compressed-tar application/gzip application/x-bzip2-compressed-tar application/x-bzip2 application/x-xz-compressed-tar application/x-xz application/x-zstd-compressed-tar application/zstd application/x-7z-compressed application/vnd.rar"
    cat > ~/.local/share/applications/unpack.desktop <<END
    [Desktop Entry]
    Type=Application
    Name=Unpack
    Comment=Extract here and remove the archive
    Exec=$HOME/.local/bin/unpack %F
    Icon=package-x-generic
    Terminal=false
    NoDisplay=true
    MimeType=$(echo $TYPES | tr ' ' ';');
    END
    update-desktop-database ~/.local/share/applications

**3. Make it the default,** keeping the old list:

    cp ~/.config/mimeapps.list ~/.config/mimeapps.list.before-unpack.bak
    xdg-mime default unpack.desktop $TYPES

Only those types: formats built on zip (`.docx`, `.epub`, `.jar`, `.apk`,
`.cbz`) have their own entries and keep their own applications. Check with
`gio mime application/epub+zip`.

File Roller stays installed, under right-click › Open With, for looking
inside an archive first or for one with a password.

### Uninstalling

    cp ~/.config/mimeapps.list.before-unpack.bak ~/.config/mimeapps.list
    rm ~/.local/share/applications/unpack.desktop
    update-desktop-database ~/.local/share/applications
