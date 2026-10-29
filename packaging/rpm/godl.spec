# Packages the already-built, already-verified binary scripts/build-rpm.sh
# places in %%{_sourcedir} — there's no source tarball, no %%prep, and
# nothing gets compiled or checked again here, so those stages are
# skipped rather than left in as no-op placeholders that would just
# confuse a reader. (%%-escaped throughout these comments: rpmbuild
# expands macros even inside a comment line, which otherwise prints a
# harmless but noisy "Macro expanded in comment" warning on every build.)
#
# %%{_version} is passed in via `rpmbuild --define "_version ..."` (see
# scripts/build-rpm.sh), read from internal/version/version.go the same
# way scripts/build-deb.sh's control file is, so both packages always
# agree on the version.
Name: godl
Version: %{_version}
Release: 1%{?dist}
Summary: Terminal download manager for HTTP(S), BitTorrent, and yt-dlp
License: MIT
URL: https://github.com/haritejakoduri/godl
BuildArch: x86_64

# A static (CGO_ENABLED=0), stripped (-ldflags="-s -w") single binary:
# no shared-library dependencies to detect, and no debuginfo to extract
# (find-debuginfo would otherwise fail loudly on a binary with no
# build-id / no symbols to pull out).
AutoReqProv: no
%global debug_package %{nil}
%global _missing_build_ids_terminate_build 0

%description
godl downloads direct HTTP(S) links, torrents, and yt-dlp-supported
social/media links through a background daemon, with a live TUI
dashboard (godl status) and scriptable pause/resume/retry/cancel/list
commands. Single static binary, no cgo.

%install
install -Dm755 %{_sourcedir}/godl %{buildroot}%{_bindir}/godl
install -Dm644 %{_sourcedir}/LICENSE %{buildroot}%{_datadir}/licenses/%{name}/LICENSE

# Application menu entry and icons, owned by rpm so they are removed
# again with the package. Exec=godl resolves because the binary goes to
# %{_bindir}, which is on every PATH.
install -Dm644 %{_sourcedir}/godl.desktop %{buildroot}%{_datadir}/applications/godl.desktop
for png in %{_sourcedir}/app*.png; do
  size=$(basename "$png" .png); size=${size#app}
  install -Dm644 "$png" %{buildroot}%{_datadir}/icons/hicolor/${size}x${size}/apps/godl.png
done
install -Dm644 %{_sourcedir}/godl.svg %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/godl.svg

%files
%{_bindir}/godl
%{_datadir}/applications/godl.desktop
%{_datadir}/icons/hicolor/*/apps/godl.*
%license %{_datadir}/licenses/%{name}/LICENSE

%post
# Best-effort refresh so the menu entry and icon appear now rather than
# after a re-login; a minimal system may have neither tool.
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q %{_datadir}/applications || :
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -qtf %{_datadir}/icons/hicolor || :
fi

echo ""
echo "godl installed. Try: godl status"

%preun
# Best-effort: stop any running godl daemon (any user) before the
# binary it's running out of gets removed. Unlike dpkg's prerm (always
# "about to remove"), rpm's %preun runs on an upgrade too — $1 is the
# number of copies of this package that will remain afterward, so 0
# means a real removal and 1+ means the new version is about to take
# over, and the daemon (about to be re-execed against the new binary
# anyway on its next start) should be left alone.
if [ "$1" = "0" ]; then
	pkill -f 'godl __daemon' 2>/dev/null || true
fi
exit 0

%postun
# Job history and cached yt-dlp/ffmpeg live per-user under
# ~/.local/share/godl, left alone here on both a plain removal and an
# upgrade — same "keep data by default" policy as scripts/uninstall.sh
# and the .deb package's postrm. dnf/rpm has no "purge" concept the way
# apt does, so there's no equivalent of the .deb postrm's purge branch:
# a user who wants that data gone runs
# "rm -rf ~/.local/share/godl" themselves.
exit 0

%changelog
