"""Adapt a writable upstream checkout for shared Android JNI/CLI use."""
import pathlib
import re
import shutil

ENVIRONMENT = 'github.com/sagernet/sing-box/common/androidcli'


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise ValueError(f'Upstream changed: expected exactly one {old!r}')
    return text.replace(old, new, 1)


def prepare_sources(repo, core):
    # Do not change the module cache or maintain a second copy of upstream APIs.
    environment = core / 'common/androidcli'
    if environment.exists():
        raise ValueError(f'Upstream now owns {environment}')
    shutil.copytree(repo / 'environment', environment)
    for original in core.rglob('*.go'):
        if original.name.endswith('_test.go') or original.is_relative_to(environment):
            continue
        text = original.read_text(encoding='utf-8')
        is_command = original.parent == core / 'cmd/sing-box'
        reads_env = re.search(r'os\.(?:Getenv|LookupEnv)\(', text)
        if not is_command and not reads_env:
            continue
        if is_command:
            text = replace_once(text, 'package main', 'package androidcli')
            if original.name == 'main.go':
                text = replace_once(text, 'func main()', 'func Run()')
            if 'func init() {' in text:
                text = text.replace('func init() {', 'func init() {\n\tif !environment.IsCLI { return }')
                text = replace_once(text, 'package androidcli',
                    f'package androidcli\n\nimport environment "{ENVIRONMENT}"')
                reads_env = False
        if reads_env:
            text, count = re.subn(r'(?m)^(package \w+)\s*$',
                rf'\1\n\nimport _ "{ENVIRONMENT}"', text, count=1)
            if count != 1:
                raise ValueError(f'Cannot insert environment bootstrap: {original}')
        original.write_text(text, encoding='utf-8')
