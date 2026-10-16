"""Adapt a writable upstream checkout for shared Android JNI/CLI use."""
import pathlib
import re
import shutil

ENVIRONMENT = 'github.com/sagernet/sing-box/common/androidcli'

# Files injected verbatim into the staged upstream checkout as
# (source-relative-to-repo, destination-relative-to-staged-core). The libbox
# AAR gains the custom URL test (urlTestWithOptions) without any change to
# reF1nd/sing-box itself.
INJECTED_FILES = (
    ('patches/daemon/url_test_options.go', 'daemon/url_test_options.go'),
    ('patches/daemon/url_test_options_test.go', 'daemon/url_test_options_test.go'),
    ('patches/daemon/url_test_http3.go', 'daemon/url_test_http3.go'),
    ('patches/daemon/url_test_http3_stub.go', 'daemon/url_test_http3_stub.go'),
    ('patches/experimental_libbox/url_test_options.go', 'experimental/libbox/url_test_options.go'),
)

# Surgical text patches applied to staged upstream files, as
# (destination-relative-to-staged-core, old, new). Each old snippet must
# occur exactly once; an upstream bump that moves it fails the build loudly
# and a human re-verifies the anchor.
#
# daemon/started_service.go: bridge the stock URLTest gRPC handler (shared by
# the in-process CommandServer and the CLI api service) to the injected
# custom measurement when the request carries the options envelope.
PATCHES = (
    (
        'daemon/started_service.go',
        '''\toutboundTag := request.OutboundTag
\toutbound, isLoaded := boxService.outboundManager.Outbound(outboundTag)''',
        '''\toutboundTag := request.OutboundTag
\tif urlTestOptions, urlTestOptionsLoaded := urlTestOptionsFromRequest(request); urlTestOptionsLoaded {
\t\treturn &emptypb.Empty{}, s.runURLTestOptions(outboundTag, urlTestOptions)
\t}
\toutbound, isLoaded := boxService.outboundManager.Outbound(outboundTag)''',
    ),
)


def patch_sources(core):
    for destination_rel, old, new in PATCHES:
        path = core / destination_rel
        if not path.is_file():
            raise ValueError(f'Missing upstream file for patch: {path}')
        text = path.read_text(encoding='utf-8')
        path.write_text(replace_once(text, old, new), encoding='utf-8')


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise ValueError(f'Upstream changed: expected exactly one {old!r}')
    return text.replace(old, new, 1)


def inject_patches(repo, core):
    # Copy each patch file to its destination in the staged checkout. The
    # destination directory must exist (it is a real upstream package);
    # refuse to create new package directories so an upstream rename is loud.
    for source_rel, destination_rel in INJECTED_FILES:
        source = repo / source_rel
        if not source.is_file():
            raise ValueError(f'Missing injected file: {source}')
        destination = core / destination_rel
        if destination.exists():
            raise ValueError(f'Upstream now owns {destination}')
        if not destination.parent.is_dir():
            raise ValueError(f'Upstream package moved, cannot inject: {destination}')
        shutil.copyfile(source, destination)


def prepare_sources(repo, core):
    # Do not change the module cache or maintain a second copy of upstream APIs.
    environment = core / 'common/androidcli'
    if environment.exists():
        raise ValueError(f'Upstream now owns {environment}')
    shutil.copytree(repo / 'environment', environment)
    inject_patches(repo, core)
    patch_sources(core)
    injected = {(core / destination_rel).resolve() for _, destination_rel in INJECTED_FILES}
    for original in core.rglob('*.go'):
        if original.name.endswith('_test.go') or original.is_relative_to(environment):
            continue
        if original.resolve() in injected:
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
