#!/usr/bin/env python3
"""Bundle the Focal ARM64 runtime; glibc and the ELF loader stay on the host."""
from pathlib import Path
import hashlib
import json
import os
import re
import shutil
import subprocess

out = Path('dist/UniTerm.AppDir').resolve()
out.mkdir(parents=True, exist_ok=False)
lib = out / 'usr/lib'
arch = Path('/usr/lib/aarch64-linux-gnu')
queue = []
copied = set()
glibc = re.compile(r'^(ld-linux.*|lib(c|m|mvec|pthread|dl|rt|util|resolv|anl|nss_[^.]+|thread_db)\.so\..*)$')

def put(src, dest):
    src, dest = Path(src), Path(dest)
    if dest in copied:
        return
    if not src.is_file():
        raise RuntimeError('Missing runtime file: ' + str(src))
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(src, dest, follow_symlinks=True)
    copied.add(dest)
    with src.open('rb') as f:
        if f.read(4) == b'\x7fELF':
            queue.append((src, dest))

def tree(src, dest):
    src = Path(src)
    if not src.is_dir():
        raise RuntimeError('Missing runtime directory: ' + str(src))
    for p in src.rglob('*'):
        if p.is_file():
            put(p, Path(dest) / p.relative_to(src))

put('bin/uniTerm', out / 'usr/bin/uniterm')
put('/tmp/kylin-smoke', out / 'usr/libexec/uniterm-smoke')
for folder in ['webkit2gtk-4.0', 'gio/modules', 'gtk-3.0', 'gdk-pixbuf-2.0',
               'gstreamer-1.0', 'gstreamer1.0']:
    tree(arch / folder, lib / 'aarch64-linux-gnu' / folder)
for name in ['bwrap', 'xdg-dbus-proxy', 'fc-cache']:
    put('/usr/bin/' + name, out / 'usr/bin' / name)
put(arch / 'libgtk-3-0/gtk-query-immodules-3.0', out / 'usr/bin/gtk-query-immodules-3.0')
for pattern in ['dri/*swrast_dri.so', 'libEGL*.so*', 'libGL*.so*', 'libGLES*.so*']:
    for p in arch.glob(pattern):
        if p.is_file():
            put(p, lib / ('dri/' + p.name if p.parent.name == 'dri' else p.name))

# ldd resolves the dependency closure in the trusted, native build container.
# Modules loaded via dlopen are explicitly queued above.
for source, dest in queue:
    result = subprocess.run(['ldd', str(source)], text=True, capture_output=True)
    if 'not found' in result.stdout:
        raise RuntimeError(str(source) + ': ' + result.stdout)
    for path in re.findall(r'(?:=>\s+|^\s*)(/[^\s]+)', result.stdout, re.M):
        p = Path(path)
        if not glibc.match(p.name):
            put(p, lib / p.name)

# Distribution WebKit builds ignore WEBKIT_EXEC_PATH. Relocate their compiled
# paths without changing string lengths. AppRun sets cwd to AppDir/usr; local
# terminal sessions independently start in the user's home/configured directory.
webkit = lib / 'libwebkit2gtk-4.0.so.37'
data = webkit.read_bytes()
patches = {}
for old in [b'/usr/lib', b'/usr/bin/bwrap', b'/usr/bin/xdg-dbus-proxy']:
    new = b'.////' + old[5:]
    assert len(old) == len(new)
    patches[old.decode()] = data.count(old)
    data = data.replace(old, new)
assert patches['/usr/lib'] > 0
webkit.write_bytes(data)

for folder in ['glib-2.0/schemas', 'mime', 'icons/Adwaita', 'icons/hicolor',
               'fonts/truetype/dejavu', 'fonts/opentype/noto', 'glvnd/egl_vendor.d']:
    tree(Path('/usr/share') / folder, out / 'usr/share' / folder)
tree('/etc/fonts', out / 'etc/fonts')
put('/etc/ssl/certs/ca-certificates.crt', out / 'etc/ssl/certs/ca-certificates.crt')
# Include redistribution notices for all packages present in the build image.
for p in Path('/usr/share/doc').glob('*/copyright'):
    if p.is_file():
        put(p, out / 'usr/share/licenses' / p.parent.name / 'copyright')
put('LICENSE', out / 'usr/share/licenses/uniterm/LICENSE')
put('build/appicon.png', out / 'uniterm.png')
put('build/kylin/AppRun', out / 'AppRun')
(out / 'AppRun').chmod(0o755)
(out / 'uniterm.desktop').write_text('[Desktop Entry]\nType=Application\nName=uniTerm\nExec=uniterm\nIcon=uniterm\nCategories=System;TerminalEmulator;\n')
(out / 'etc/fonts/fonts.conf').write_text('''<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
<dir prefix="relative">../../usr/share/fonts</dir>
<dir>/usr/share/fonts</dir><dir prefix="xdg">fonts</dir>
<cachedir prefix="xdg">fontconfig</cachedir>
<include ignore_missing="yes">conf.d</include>
</fontconfig>
''')

manifest = []
for source, dest in queue:
    header = subprocess.check_output(['readelf', '-h', str(dest)], text=True)
    assert 'AArch64' in header, str(dest)
    versions = subprocess.check_output(['readelf', '--version-info', str(dest)], text=True)
    required = [tuple(map(int, v.split('.'))) for v in re.findall(r'GLIBC_([0-9.]+)', versions)]
    if required and max(required) > (2, 31):
        raise RuntimeError('Requires newer glibc: ' + str(dest))
    relative = os.path.relpath(lib, dest.parent)
    rpath = '$ORIGIN' if relative == '.' else '$ORIGIN/' + relative
    subprocess.run(['patchelf', '--force-rpath', '--set-rpath', rpath, str(dest)], check=True)
    manifest.append({'file': str(dest.relative_to(out)), 'max_glibc': max(required) if required else None,
                     'sha256': hashlib.sha256(dest.read_bytes()).hexdigest()})
(out / 'runtime-manifest.json').write_text(json.dumps({'glibc_baseline': '2.31', 'webkit': '2.38',
    'path_relocations': patches, 'elf_files': manifest}, indent=2))
print('Bundled', len(queue), 'ELF files; all require glibc <= 2.31', flush=True)
