"""Exercise the real standalone core and its embedded helper on a rootable AVD.

Requires an x86_64 AAR, its javac/D8 helper dex from build staging, and adb.
Build with --work-dir to retain the dex at
<work-dir>/androidlibbox-staging/system-dns-dex/classes.dex.
Owns only /data/local/tmp/libbox-system-dns-test and an optional dedicated home.
Temporarily toggles Wi-Fi/data and restores their original settings. While the
core runs, DNS sockets not owned by the core's GID 3005 are rejected, so lookups
must come from the core itself rather than Android's resolver.
"""
import argparse
import hashlib
import json
import pathlib
import shlex
import socket
import ssl
import subprocess
import tempfile
import time
import zipfile

PORT = 18081
CORE_GID = '3005'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--serial', required=True)
    parser.add_argument('--aar', required=True, type=pathlib.Path)
    parser.add_argument('--dex', required=True, type=pathlib.Path)
    parser.add_argument('--home', help='Dedicated test directory, including under app filesDir')
    parser.add_argument('--restart-system-server', action='store_true',
                        help='Also kill system_server (restarts the Android framework)')
    args = parser.parse_args()
    adb = ['adb', '-s', args.serial]
    remote = '/data/local/tmp/libbox-system-dns-test'
    home = args.home or remote + '/home'
    helper_file = home + '/system-dns-helper.dex'
    core = remote + '/libsing-box.so'

    def call(*command, check=True):
        result = subprocess.run(adb + list(command), text=True, capture_output=True, timeout=40)
        if check and result.returncode:
            raise RuntimeError((command, result.stdout, result.stderr))
        return result.stdout.strip()

    def shell(*command, check=True):
        return call('shell', shlex.join(command), check=check)

    def until(action, timeout=20):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                result = action()
                if result:
                    return result
            except (OSError, ValueError):
                pass
            time.sleep(.1)
        raise AssertionError('Timed out: ' + str(action))

    def alive(pid):
        value = shell('cat', '/proc/' + str(pid) + '/stat', check=False)
        return bool(value) and not value.split(') ', 1)[1].startswith('Z')

    def connect(name, port=443):
        # SOCKS5 CONNECT by name, so the core performs the lookup.
        proxy = socket.create_connection(('127.0.0.1', PORT), timeout=15)
        try:
            proxy.settimeout(15)
            proxy.sendall(b'\x05\x01\x00')
            if proxy.recv(2) != b'\x05\x00':
                raise OSError('SOCKS greeting rejected')
            host = name.encode()
            proxy.sendall(b'\x05\x01\x00\x03' + bytes([len(host)]) + host + port.to_bytes(2, 'big'))
            reply = b''
            while len(reply) < 10:
                part = proxy.recv(10 - len(reply))
                if not part:
                    raise OSError('SOCKS closed before reply')
                reply += part
            if reply[1] != 0:
                raise OSError('SOCKS CONNECT failed: %d' % reply[1])
            return proxy
        except BaseException:
            proxy.close()
            raise

    def resolved():
        try:
            connect('example.com').close()
            return True
        except OSError:
            return False

    def helper_pid(owner):
        for line in shell('ps', '-A', '-o', 'PID,PPID,NAME').splitlines()[1:]:
            pid, parent, name = line.split(maxsplit=2)
            if int(parent) == owner and name == 'libbox-system-dns':
                return int(pid)
        return None

    assert shell('getprop', 'ro.kernel.qemu') == '1', 'Use an AVD'
    call('root')
    call('wait-for-device')
    assert shell('id', '-u') == '0'
    assert shell('getenforce') == 'Enforcing'
    initial = {kind: shell('settings', 'get', 'global', setting) != '0'
               for kind, setting in [('wifi', 'wifi_on'), ('data', 'mobile_data')]}
    shell('mkdir', '-p', remote, home)
    expected = hashlib.sha256(args.dex.read_bytes()).hexdigest()
    process = None
    owner = observer = None
    block = [[table, '-p', protocol, '--dport', '53', '-m', 'owner', '!', '--gid-owner', CORE_GID,
              '-j', 'REJECT'] for table in ('iptables', 'ip6tables') for protocol in ('udp', 'tcp')]

    with tempfile.TemporaryDirectory() as tempdir:
        temp = pathlib.Path(tempdir)
        with zipfile.ZipFile(args.aar) as aar:
            for name in ('libbox.so', 'libsing-box.so'):
                file = temp / name
                file.write_bytes(aar.read('jni/x86_64/' + name))
                call('push', str(file), remote + '/' + name)
        shell('chmod', '755', core)
        config = temp / 'config.json'
        config.write_text(json.dumps({
            'log': {'level': 'warn'},
            'dns': {'servers': [{'type': 'local', 'tag': 'local'}], 'final': 'local'},
            'inbounds': [{'type': 'socks', 'listen': '127.0.0.1', 'listen_port': PORT}],
            'outbounds': [{'type': 'direct', 'tag': 'direct'}],
            'route': {'default_domain_resolver': 'local', 'final': 'direct'},
        }))
        call('push', str(config), home + '/config.json')
        call('forward', 'tcp:' + str(PORT), 'tcp:' + str(PORT))
        logs = open(temp / 'core.log', 'w+')

        def start():
            nonlocal process, owner, observer
            shell('rm', '-f', remote + '/core.pid')
            # asteriskd runs cores as UID 0 / GID 3005 with the home as cwd.
            command = ('cd ' + shlex.quote(home) + ' && echo $$ > ' + shlex.quote(remote + '/core.pid') +
                       ' && exec su 0,' + CORE_GID + ' ' +
                       shlex.join([core, 'run', '--disable-color', '-D', home, '-c', home + '/config.json']))
            process = subprocess.Popen(adb + ['shell', '-T', command], stdin=subprocess.DEVNULL,
                                       stdout=logs, stderr=subprocess.STDOUT)
            owner = int(until(lambda: shell('cat', remote + '/core.pid', check=False)))
            until(resolved, timeout=60)
            observer = until(lambda: helper_pid(owner))
            assert shell('sha256sum', helper_file).split()[0] == expected
            assert shell('stat', '-c', '%a %u', helper_file) == '400 0'
            return observer

        def stop():
            if process is not None and process.poll() is None:
                shell('kill', '-TERM', str(owner))
                assert process.wait(timeout=10) == 0
            if observer is not None:
                until(lambda: not alive(observer))

        try:
            for rule in block:
                shell(rule[0], '-I', 'OUTPUT', *rule[1:])
            # Use a separate empty home so a previous retained cache is harmless.
            tools_home = home + '/tools-home'
            shell('mkdir', '-p', tools_home)
            for flags in [('version',), ('check', '-c', home + '/config.json'),
                          ('format', '-c', home + '/config.json')]:
                shell(core, '-D', tools_home, *flags)
                assert not shell('ls', tools_home + '/system-dns-helper.dex', check=False)
            print('PASS tool commands do not create helper dex', flush=True)
            start()
            with connect('example.org') as proxy:
                with ssl.create_default_context().wrap_socket(proxy, server_hostname='example.org') as tls:
                    assert tls.version()
            print('PASS local DNS through the core while non-core DNS is rejected', flush=True)
            assert shell('readlink', '/proc/' + str(observer) + '/fd/0') == shell(
                'readlink', '/proc/' + str(observer) + '/fd/3')
            print('PASS inherited socketpair supplies snapshots and parent lifeline', flush=True)
            for kind in ('data', 'wifi'):
                shell('svc', kind, 'disable')
            until(lambda: not resolved(), timeout=30)
            for kind in ('data', 'wifi'):
                shell('svc', kind, 'enable')
            until(resolved, timeout=60)
            print('PASS offline empty snapshot and online DNS recovery', flush=True)
            identity = shell('stat', '-c', '%i %Y', helper_file)
            stop()
            assert shell('sha256sum', helper_file).split()[0] == expected
            start()
            assert shell('stat', '-c', '%i %Y', helper_file) == identity
            stop()
            print('PASS graceful exit retains dex; restart reuses inode and mtime', flush=True)
            corrupt = temp / 'stale.dex'
            data = bytearray(args.dex.read_bytes())
            data[-1] ^= 1
            corrupt.write_bytes(data)
            call('push', str(corrupt), helper_file)
            start()
            assert shell('stat', '-c', '%i %Y', helper_file) != identity
            print('PASS same-size stale dex replaced before ART loads it', flush=True)
            shell('kill', '-KILL', str(observer))
            begin = time.monotonic()
            # The forced exit only fires after ten seconds of stalled shutdown.
            assert process.wait(timeout=15) == 70
            assert time.monotonic() - begin < 8
            until(lambda: not alive(observer))
            print('PASS helper failure shuts core down gracefully with status 70', flush=True)
            start()
            shell('kill', '-STOP', str(observer))
            shell('kill', '-KILL', str(owner))
            assert process.wait(timeout=10) == 137
            until(lambda: not alive(observer))
            print('PASS core SIGKILL also kills stopped helper via pinned-thread PDEATHSIG', flush=True)
            if args.restart_system_server:
                start()
                server = shell('pidof', 'system_server')
                shell('kill', '-KILL', server)
                assert process.wait(timeout=60) == 70
                until(lambda: not alive(observer))
                logs.flush()
                assert 'Connectivity service died' in (temp / 'core.log').read_text(errors='replace')
                # svc needs the restarted framework's Wi-Fi service before cleanup.
                until(lambda: shell('pidof', 'system_server', check=False) not in ('', server) and
                      shell('getprop', 'sys.boot_completed', check=False) == '1' and
                      'not found' not in shell('service', 'check', 'wifi', check=False), timeout=180)
                print('PASS system_server restart exits core instead of keeping stale DNS', flush=True)
        except BaseException:
            logs.flush()
            logs.seek(0)
            print(logs.read())
            raise
        finally:
            for rule in block:
                shell(rule[0], '-D', 'OUTPUT', *rule[1:], check=False)
            # A freshly restarted framework may reject svc briefly.
            for kind, enabled in initial.items():
                command = shlex.join(['svc', kind, 'enable' if enabled else 'disable'])
                until(lambda: subprocess.run(adb + ['shell', command], capture_output=True,
                                             timeout=40).returncode == 0, timeout=60)
            if process is not None and process.poll() is None:
                stop()
            logs.close()
            call('forward', '--remove', 'tcp:' + str(PORT), check=False)


if __name__ == '__main__':
    main()
