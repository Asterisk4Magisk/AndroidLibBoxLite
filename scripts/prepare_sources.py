"""Adapt a writable upstream checkout for shared Android JNI/CLI use."""
import json
import re
import shutil
import stat
import subprocess
from pathlib import Path

from reality_compat import REALITY_FILES, REALITY_PATCHES

ENVIRONMENT = 'github.com/sagernet/sing-box/common/androidcli'

# Files injected verbatim into the staged upstream checkout as
# (source-relative-to-repo, destination-relative-to-staged-core). The libbox
# AAR gains the custom URL test (urlTestWithOptions) without any change to
# reF1nd/sing-box itself.
INJECTED_FILES = (
    ('patches/process/flow_owner.go', 'common/process/asterisk_flow_owner.go'),
    ('patches/protocol_socks/flow.go', 'protocol/socks/asterisk_flow.go'),
    *REALITY_FILES,
    ('patches/daemon/proxy_ip_probe.go', 'daemon/proxy_ip_probe.go'),
    ('patches/daemon/proxy_ip_probe.proto', 'daemon/proxy_ip_probe.proto'),
    ('patches/daemon/proxy_ip_probe.pb.go', 'daemon/proxy_ip_probe.pb.go'),
    ('patches/daemon/proxy_ip_probe_grpc.pb.go', 'daemon/proxy_ip_probe_grpc.pb.go'),
    ('patches/experimental_libbox/proxy_ip_probe.go', 'experimental/libbox/proxy_ip_probe.go'),
    ('patches/daemon/url_test_options.go', 'daemon/url_test_options.go'),
    ('patches/daemon/url_test_http3.go', 'daemon/url_test_http3.go'),
    ('patches/daemon/url_test_http3_stub.go', 'daemon/url_test_http3_stub.go'),
    ('patches/experimental_libbox/url_test_options.go', 'experimental/libbox/url_test_options.go'),
    ('patches/dns_systemconfig/source_managed.go', 'dns/transport/local/systemconfig/source_managed.go'),
    ('patches/experimental_libbox/command_server_plugins.go', 'experimental/libbox/command_server_plugins.go'),
)

def proxy_ip_patches():
    yield (
        'daemon/server.go',
        '\tRegisterStartedServiceServer(server, startedService)',
        '\tRegisterStartedServiceServer(server, startedService)\n\tRegisterProxyIPProbe(server, startedService, ".")',
    )
    yield (
        'experimental/libbox/command_server.go',
        '\tdaemon.RegisterStartedServiceServer(s.grpcServer, s.StartedService)',
        '\tdaemon.RegisterStartedServiceServer(s.grpcServer, s.StartedService)\n'
        '\tdaemon.RegisterProxyIPProbe(s.grpcServer, s.StartedService, sBasePath)',
    )
    urltest = 'protocol/group/urltest.go'
    balance = 'protocol/group/loadbalance.go'
    selector = 'protocol/group/selector.go'
    block = 'protocol/block/outbound.go'
    for path in (urltest, balance, selector, block):
        yield (path, 'import (', 'import (\n\t"github.com/sagernet/sing-box/common/androidcli/proxyip"')

    for receiver, path in (('URLTest', urltest), ('LoadBalance', balance)):
        for method, args, result in (
            ('DialContext', 'network string, destination M.Socksaddr', 'net.Conn'),
            ('ListenPacket', 'destination M.Socksaddr', 'net.PacketConn'),
        ):
            head = f'func (s *{receiver}) {method}(ctx context.Context, {args}) ({result}, error) {{'
            yield (path, head + '\n\ts.group.Touch()', head + '\n'
                   '\treadOnly := proxyip.ReadOnly(ctx)\n'
                   '\tif !readOnly {\n\t\ts.group.Touch()\n\t}')
    for network in ('network', 'N.NetworkUDP'):
        head = f'outbound := s.group.Unwrap(&metadata, true)\n\tif outbound == nil || !common.Contains(outbound.Network(), {network})'
        yield (balance, head, head.replace('Unwrap(&metadata, true)', 'dialOutbound(&metadata, readOnly)'))

    for path, on_failure in (
        (urltest, 's.group.history.DeleteURLTestHistory(outbound.Tag())'),
        (balance, 's.OnConnectionFailure(ctx)'),
    ):
        for method, args in (
            ('NewConn', 'conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsResourceDownloadFromContext(ctx)'),
            ('NewPacketConn', 'conn, interrupt.IsExternalConnectionFromContext(ctx), interrupt.IsResourceDownloadFromContext(ctx)'),
        ):
            head = 'if err == nil {\n\t\treturn s.group.interruptGroup.' + method + '(' + args + '), nil\n\t}'
            failure = '\n\ts.logger.ErrorContext(ctx, err)\n\t' + on_failure
            yield (path, head + failure,
                   'if readOnly {\n\t\treturn conn, err\n\t}\n\t' + head + failure)

    for method, params, args, result in (
        ('DialContext', 'network string, destination M.Socksaddr', 'network, destination', 'net.Conn'),
        ('ListenPacket', 'destination M.Socksaddr', 'destination', 'net.PacketConn'),
    ):
        head = f'func (s *Selector) {method}(ctx context.Context, {params}) ({result}, error) {{'
        call = f'\n\tconn, err := s.selected.Load().{method}(ctx, {args})'
        yield (selector, head + call, head + call + '\n\tif proxyip.ReadOnly(ctx) {\n\t\treturn conn, err\n\t}')
        head = f'func (h *Outbound) {method}(ctx context.Context, {params}) ({result}, error) {{'
        yield (block, head, head + '\n\tif proxyip.ReadOnly(ctx) {\n\t\treturn nil, proxyip.ErrBlocked\n\t}')

    yield (balance, '\tstrategyFn      strategyFn\n',
           '\tstrategyFn      strategyFn\n\tprobeStrategy   func(*adapter.InboundContext) adapter.Outbound\n')
    unwrap = 'func (g *LoadBalanceGroup) Unwrap(metadata *adapter.InboundContext, touch bool) adapter.Outbound {'
    yield (balance, unwrap,
           '''func (g *LoadBalanceGroup) dialOutbound(metadata *adapter.InboundContext, readOnly bool) adapter.Outbound {
	if readOnly {
		return g.probeStrategy(metadata)
	}
	return g.Unwrap(metadata, true)
}

func (g *LoadBalanceGroup) bindStrategy(pick func(*adapter.InboundContext, bool, outboundMatcher, bool) adapter.Outbound) strategyFn {
	g.probeStrategy = func(metadata *adapter.InboundContext) adapter.Outbound {
		return pick(metadata, false, nil, true)
	}
	return func(metadata *adapter.InboundContext, touch bool, matcher outboundMatcher) adapter.Outbound {
		return pick(metadata, touch, matcher, false)
	}
}

''' + unwrap)
    strategy_args = 'metadata *adapter.InboundContext, touch bool, matcher outboundMatcher'
    for first_line in (
        'idxMutex.Lock()',
        'outbounds := g.loadOutbounds()\n\t\tkey := hash.Hash(getKey(metadata))',
        'outbounds := g.loadOutbounds()\n\t\tkey := hash.Hash(getKeyWithSrcAndDst(metadata))',
    ):
        anchor = f'\treturn func({strategy_args}) adapter.Outbound {{\n\t\t{first_line}'
        yield (balance, anchor, anchor.replace('return func(', 'return g.bindStrategy(func(')
               .replace('matcher outboundMatcher)', 'matcher outboundMatcher, readOnly bool)'))
    yield (balance, 'func (g *LoadBalanceGroup) nextFallback(outbounds []adapter.Outbound, touch bool, matcher outboundMatcher)',
           'func (g *LoadBalanceGroup) nextFallback(outbounds []adapter.Outbound, touch bool, matcher outboundMatcher, readOnly bool)')
    yield (balance, 'if matcher == nil || touch {\n\t\tg.fallbackIdx.Store(nextIndex)',
           'if !readOnly && (matcher == nil || touch) {\n\t\tg.fallbackIdx.Store(nextIndex)')
    yield (balance, 'return g.nextFallback(outbounds, touch, matcher)\n\t}\n}\n\nfunc strategyConsistentHashing',
           'return g.nextFallback(outbounds, touch, matcher, readOnly)\n\t})\n}\n\nfunc strategyConsistentHashing')
    yield (balance, 'return g.nextFallback(outbounds, touch, matcher)\n\t}\n}\n\nfunc strategyStickySessions',
           'return g.nextFallback(outbounds, touch, matcher, readOnly)\n\t})\n}\n\nfunc strategyStickySessions')
    yield (balance, 'if matcher == nil {\n\t\t\tidx, has = lruCache.Get(key)',
           'if readOnly {\n\t\t\tidx, has = lruCache.PeekReadOnly(key)\n'
           '\t\t} else if matcher == nil {\n\t\t\tidx, has = lruCache.Get(key)')
    yield (balance, '''				if !validMapping || nowIdx != idx {
					lruCache.Add(key, nowIdx)
				} else if matcher != nil {
					lruCache.Get(key)
				}''',
           '''				if !readOnly {
					if !validMapping || nowIdx != idx {
						lruCache.Add(key, nowIdx)
					} else if matcher != nil {
						lruCache.Get(key)
					}
				}''')
    yield (balance, '\t\tlruCache.Add(key, fbIdx)',
           '\t\tif !readOnly {\n\t\t\tlruCache.Add(key, fbIdx)\n\t\t}')
    yield (balance, '\t\treturn outbounds[fbIdx]\n\t}\n}', '\t\treturn outbounds[fbIdx]\n\t})\n}')


# Surgical text patches applied to staged upstream files, as
# (destination-relative-to-staged-core, old, new). Each old snippet must
# occur exactly once; an upstream bump that moves it fails the build loudly
# and a human re-verifies the anchor.
#
# daemon/started_service.go: bridge the stock URLTest gRPC handler (shared by
# the in-process CommandServer and the CLI api service) to the injected
# custom measurement when the request carries the options envelope (urltest
# group targets stay on the stock CheckOutbounds path).
PATCHES = (
    # TPROXY UDP's OriginDestination is its listener address. Ownership needs
    # the actual packet destination, before the router rewrites it.
    (
        'route/process_cache.go',
        '\t"github.com/sagernet/sing-box/adapter"\n',
        '\t"github.com/sagernet/sing-box/adapter"\n'
        '\tC "github.com/sagernet/sing-box/constant"\n',
    ),
    (
        'route/process_cache.go',
        '\tif metadata.OriginDestination.IsValid() {\n',
        '\tif metadata.InboundType == C.TypeTProxy && metadata.Destination.IsIP() {\n'
        '\t\toriginDestination = metadata.Destination.AddrPort()\n'
        '\t} else if metadata.OriginDestination.IsValid() {\n',
    ),
    (
        'protocol/socks/inbound.go',
        'func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SocksInboundOptions) (adapter.Inbound, error) {\n',
        'func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SocksInboundOptions) (adapter.Inbound, error) {\n'
        '\tfor _, user := range options.Users {\n\t\tif user.Username == "__asterisk_flow_v1" {\n\t\t\treturn newFlowInbound(ctx, router, logger, tag, options, user.Password)\n\t\t}\n\t}\n',
    ),
    (
        'route/process_cache.go',
        'func (r *Router) searchProcessInfo(ctx context.Context, metadata *adapter.InboundContext) {\n',
        'func (r *Router) searchProcessInfo(ctx context.Context, metadata *adapter.InboundContext) {\n'
        '\tif metadata.ProcessInfo != nil && r.network != nil {\n\t\tprocess.CompleteFlowOwner(metadata.ProcessInfo, r.network.PackageManager())\n\t}\n',
    ),
    *REALITY_PATCHES,
    *proxy_ip_patches(),
    (
        'daemon/started_service.go',
        '''\toutboundTag := request.OutboundTag
\toutbound, isLoaded := boxService.outboundManager.Outbound(outboundTag)''',
        '''\toutboundTag := request.OutboundTag
\tif urlTestOptionsHandled, urlTestOptionsErr := s.handleURLTestOptions(ctx, request); urlTestOptionsHandled {
\t\tif urlTestOptionsErr != nil {
\t\t\treturn nil, urlTestOptionsErr
\t\t}
\t\treturn &emptypb.Empty{}, nil
\t}
\toutbound, isLoaded := boxService.outboundManager.Outbound(outboundTag)''',
    ),
    # Android has no resolv.conf; standalone ROOT supplies the physical
    # network's DNS servers through its own observer instead.
    (
        'dns/transport/local/systemconfig/source_resolv.go',
        'func (s *Source) Configuration() *Config {\n',
        'func (s *Source) Configuration() *Config {\n'
        '\tif config := managedConfiguration(); config != nil {\n'
        '\t\treturn config\n'
        '\t}\n',
    ),
    # Start the observer and the plugins only for a running service, after the
    # run loop's signal handler exists so helper or plugin loss always shuts
    # down gracefully.
    (
        'cmd/sing-box/cmd_run.go',
        '\t"github.com/sagernet/sing-box/adapter"\n',
        '\t"github.com/sagernet/sing-box/adapter"\n'
        '\tandroidpluginpool "github.com/sagernet/sing-box/common/androidcli/pluginpool"\n'
        '\tandroidsystemdns "github.com/sagernet/sing-box/common/androidcli/systemdns"\n',
    ),
    (
        'cmd/sing-box/cmd_run.go',
        '\tsignal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)\n'
        '\tdefer signal.Stop(osSignals)\n'
        '\tfor {\n',
        '\tsignal.Notify(osSignals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)\n'
        '\tdefer signal.Stop(osSignals)\n'
        '\tdefer androidsystemdns.StartService(".")()\n'
        '\tdefer androidpluginpool.StartService(configPaths)()\n'
        '\tfor {\n',
    ),
    # Plugins belong to the service: stop them wherever upstream stops it.
    (
        'experimental/libbox/command_server.go',
        'func (s *CommandServer) Close() {\n',
        'func (s *CommandServer) Close() {\n'
        '\ts.closePlugins()\n',
    ),
    (
        'experimental/libbox/command_server.go',
        'func (s *CommandServer) CloseService() error {\n',
        'func (s *CommandServer) CloseService() error {\n'
        '\ts.closePlugins()\n',
    ),
)


def patch_sources(core):
    # Keep executable-path lookup upstream, but share the bridge's binary
    # UID/inode query and UDP ambiguity handling with the other Android cores.
    path = core / 'common/process/searcher_linux.go'
    text = path.read_text(encoding='utf-8')
    start = text.index('func (s *linuxSearcher) resolveSocketByNetlink(')
    end = text.index('\nfunc buildProcessPaths(', start)
    text = text[:start] + '''func (s *linuxSearcher) resolveSocketByNetlink(network string, source netip.AddrPort, destination netip.AddrPort) (inode, uid uint32, err error) {
    return resolveFlowSocket(network, source, destination)
}
''' + text[end:]
    path.write_text(text, encoding='utf-8')
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
    shutil.copytree(repo / 'native/flowmeta', environment / 'flowmeta')
    # build_shared.py generates this package's embedded helper dex.
    shutil.copytree(repo / 'native/systemdns', environment / 'systemdns',
                    ignore=shutil.ignore_patterns('*_test.go', 'java'))
    shutil.copytree(repo / 'native/pluginpool', environment / 'pluginpool',
                    ignore=shutil.ignore_patterns('*_test.go'))
    shutil.copytree(repo / 'native/proxyip', environment / 'proxyip')
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


def prepare_probe_cache(repo, core, env):
    # The upstream Peek evicts expired entries. Add a read-only accessor to
    # the pinned dependency in staging so the stock cache calls remain intact.
    module = 'github.com/sagernet/sing'
    manifest = subprocess.check_output(['go', 'mod', 'download', '-json', module], cwd=core, env=env)
    source = Path(json.loads(manifest)['Dir'])
    staged = core.parent / 'sing-probe-cache'
    shutil.copytree(source, staged, copy_function=shutil.copyfile)
    package = staged / 'contrab/freelru'
    package.chmod(package.stat().st_mode | stat.S_IWUSR)
    accessor = package / 'asterisk_peek_read_only.go'
    if accessor.exists():
        raise ValueError(f'Upstream now owns {accessor}')
    shutil.copyfile(repo / 'patches/freelru/peek_read_only.go', accessor)
    subprocess.run(['go', 'mod', 'edit', f'-replace={module}={staged.resolve()}'],
                   cwd=core, env=env, check=True)
