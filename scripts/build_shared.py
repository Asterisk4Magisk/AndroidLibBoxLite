"""Build gomobile bindings plus a CLI sharing one sing-box library (Python 3.10+)."""
import argparse
import json
import os
import pathlib
import platform
import re
import shutil
import stat
import subprocess
import tempfile
import zipfile

from prepare_sources import prepare_sources
from check_upstream import pinned, MODULE, FORK

ARCHES = {
    'arm': ('armeabi-v7a', 'armv7a-linux-androideabi'),
    'arm64': ('arm64-v8a', 'aarch64-linux-android'),
    '386': ('x86', 'i686-linux-android'),
    'amd64': ('x86_64', 'x86_64-linux-android'),
}


def copy_writable(source, target):
    target = pathlib.Path(target)
    if target.exists():
        target.chmod(stat.S_IREAD | stat.S_IWRITE)
    return shutil.copyfile(source, target)


def writable_directories(root):
    # copytree preserves the Unix module cache's read-only directory modes.
    for directory, _, _ in os.walk(root, followlinks=False):
        path = pathlib.Path(directory)
        path.chmod(path.stat().st_mode | stat.S_IRWXU)


def reset_staging(work):
    stage = work / 'androidlibbox-staging'
    marker = stage / '.owned-by-build-shared'
    if stage.exists():
        if stage.is_symlink() or stage.resolve().parent != work.resolve() or not marker.is_file():
            raise ValueError(f'Refusing to replace unrecognized staging directory: {stage}')
        writable_directories(stage)
        shutil.rmtree(stage)
    stage.mkdir()
    marker.touch()
    return stage


def run(command, cwd, env, log=None):
    print('+ ' + ' '.join(map(str, command)), flush=True)
    if log:
        # Retain live compiler diagnostics even if the build is interrupted.
        with pathlib.Path(log).open('w', encoding='utf-8') as output:
            result = subprocess.run(list(map(str, command)), cwd=cwd, env=env,
                                    stdout=output, stderr=subprocess.STDOUT)
        output = pathlib.Path(log).read_text(encoding='utf-8', errors='replace')
        if result.returncode:
            raise RuntimeError(output)
        return output.strip()
    result = subprocess.run(list(map(str, command)), cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if result.returncode:
        raise RuntimeError(result.stdout)
    return result.stdout.strip()


def package_aar(baseline, target, artifacts):
    replacements = {}
    for abi, (core, launcher) in artifacts.items():
        replacements[f'jni/{abi}/libbox.so'] = core
        replacements[f'jni/{abi}/libsing-box.so'] = launcher
    with zipfile.ZipFile(baseline) as original, zipfile.ZipFile(target, 'w', zipfile.ZIP_DEFLATED) as package:
        expected = {f'jni/{abi}/libbox.so' for abi in artifacts}
        actual = {name for name in original.namelist() if name.startswith('jni/') and name.endswith('.so')}
        if actual != expected:
            raise ValueError(f'Unexpected gomobile native entries: {actual}')
        for entry in original.infolist():
            if entry.filename not in replacements:
                package.writestr(entry, original.read(entry))
        for name, path in replacements.items():
            entry = zipfile.ZipInfo(name)
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.external_attr = 0o100755 << 16
            package.writestr(entry, path.read_bytes())


def build(args, work):
    repo = pathlib.Path(__file__).resolve().parents[1]
    env = os.environ.copy()
    for name in ('GOOS', 'GOARCH', 'GOARM', 'CC', 'CXX', 'CGO_ENABLED'):
        env.pop(name, None)
    temp = work / 'tmp'
    temp.mkdir(parents=True, exist_ok=True)
    env.update(TEMP=str(temp), TMP=str(temp), TMPDIR=str(temp))
    ndk = pathlib.Path(env['ANDROID_NDK_HOME']).resolve()
    host = {'Windows': 'windows-x86_64', 'Linux': 'linux-x86_64', 'Darwin': 'darwin-x86_64'}[platform.system()]
    suffix = '.exe' if os.name == 'nt' else ''
    clang = ndk / 'toolchains/llvm/prebuilt' / host / ('bin/clang' + suffix)
    if not clang.is_file():
        raise ValueError(f'NDK clang missing: {clang}')
    version = pinned(repo)
    module = json.loads(run(['go', 'mod', 'download', '-json', MODULE], repo, env))
    if module.get('Path') != FORK or module.get('Version') != version:
        raise ValueError('Downloaded module does not match the pinned fork')
    stage = reset_staging(work)
    source = stage / 'sing-box'
    shutil.copytree(module['Dir'], source, copy_function=copy_writable)
    writable_directories(source)
    # JNI and CLI share the explicitly maintained local feature set.
    tags = (repo / 'build_tags.txt').read_text().strip()
    if not tags or any(not re.fullmatch(r'[A-Za-z0-9_]+', tag) for tag in tags.split(',')):
        raise ValueError('Invalid build_tags.txt')
    prepare_sources(repo, source)
    mobile = run(['go', 'list', '-m', '-f', '{{.Version}}', 'github.com/sagernet/gomobile'], repo, env)
    # Build in upstream's module so its fork-specific dependency replacements survive.
    run(['go', 'mod', 'edit', '-go=' + run(['go', 'list', '-m', '-f', '{{.GoVersion}}'], repo, env),
         '-require=github.com/sagernet/gomobile@' + mobile], source, env)
    run(['go', 'mod', 'download', 'github.com/sagernet/gomobile'], source, env)
    ldflags = (f'-s -w -buildid= -X github.com/sagernet/sing-box/constant.Version={version[1:]} '
               + (source / 'release/LDFLAGS').read_text().strip())
    tools = work / 'tools'
    tools.mkdir(exist_ok=True)
    env['GOBIN'] = str(tools)
    env['PATH'] = str(tools) + os.pathsep + env['PATH']
    env['CGO_LDFLAGS'] = '-Wl,-z,max-page-size=16384'
    for name in ('gomobile', 'gobind'):
        run(['go', 'install', f'github.com/sagernet/gomobile/cmd/{name}@{mobile}'], source, env)
    baseline = stage / 'baseline.aar'
    output = run([tools / ('gomobile' + suffix), 'bind', '-work', '-target',
        ','.join('android/' + arch for arch in args.arch), '-androidapi', str(args.api),
        '-javapkg=io.nekohasekai', '-libname=box', '-tags=' + tags, '-trimpath',
        '-buildvcs=false', '-ldflags=' + ldflags, '-o', baseline, './experimental/libbox'], source, env, work / 'gomobile.log')
    matches = re.findall(r'^WORK=(.+)$', output, re.MULTILINE)
    if not matches:
        raise ValueError('gomobile did not report its work directory')
    generated = pathlib.Path(matches[-1].strip())
    jni = stage / 'jni'
    artifacts = {}
    for arch in args.arch:
        abi, triple = ARCHES[arch]
        output = jni / abi
        output.mkdir(parents=True, exist_ok=True)
        target = f'--target={triple}{args.api}'
        goenv = dict(env, GOOS='android', GOARCH=arch, GOARM='7', CGO_ENABLED='1',
                     CC=f'"{clang}" {target}', CGO_LDFLAGS='-Wl,-z,max-page-size=16384')
        bound = source / 'build' / arch / 'shared'
        shutil.copytree(generated / 'src/gobind', bound)
        shutil.copyfile(repo / 'native/cli/export.go.txt', bound / 'cli_export.go')
        run(['go', 'build', '-mod=mod', '-trimpath', '-buildvcs=false',
             '-tags=' + tags, '-ldflags=' + ldflags, '-buildmode=c-shared',
             '-o', output / 'libbox.so', '.'], bound, goenv, work / (abi + '.log'))
        common = [clang, target, '-O2', '-fPIC', '-Wl,-z,max-page-size=16384', '-Wl,--build-id=none', '-Wl,-s']
        run(common + ['-fPIE', '-pie', '-Wall', '-Wextra', '-Werror',
            '-o', output / 'libsing-box.so', repo / 'native/cli/launcher.c', '-ldl'], repo, env)
        artifacts[abi] = (output / 'libbox.so', output / 'libsing-box.so')
    aar = stage / 'libbox.aar'
    package_aar(baseline, aar, artifacts)
    target = pathlib.Path(args.output).resolve()
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(aar, target)
    shutil.copyfile(stage / 'baseline-sources.jar', target.with_name(target.stem + '-sources.jar'))
    (work / 'build-info.json').write_text(json.dumps({'core': version, 'abis': args.arch,
        'mobile': mobile, 'gomobile_work': str(generated),
        'tags': tags, 'ldflags': ldflags, 'output': str(target)}, indent=2))
    print(f'Built {target}', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--arch', nargs='+', choices=ARCHES, default=list(ARCHES))
    parser.add_argument('--api', type=int, default=23)
    parser.add_argument('--output', default='libbox.aar')
    parser.add_argument('--work-dir', help='Retain native artifacts, generated sources and build logs')
    args = parser.parse_args()
    if args.api < 23 or len(args.arch) != len(set(args.arch)):
        parser.error('API must be >= 23 and architectures must be unique')
    if args.work_dir:
        work = pathlib.Path(args.work_dir).resolve()
        work.mkdir(parents=True, exist_ok=True)
        build(args, work)
    else:
        with tempfile.TemporaryDirectory(prefix='box-shared-') as directory:
            build(args, pathlib.Path(directory))


if __name__ == '__main__':
    main()
