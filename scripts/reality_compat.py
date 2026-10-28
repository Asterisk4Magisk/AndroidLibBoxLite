"""Temporary Xray 26.10.21 fingerprint/REALITY compatibility, staging only.

Keep the native uTLS offers, including randomized ones, exactly as generated.
Remove this module and patches/tls/reality_fingerprints.go once upstream owns
the equivalent behavior. The dependency override is pinned in the root go.mod.
"""
import json
import subprocess
import zipfile

UTLS_MODULE = 'github.com/metacubex/utls'
UPSTREAM_UTLS_VERSION = 'v1.8.7'

REALITY_FILES = (
    ('patches/tls/reality_fingerprints.go', 'common/tls/androidlibbox_fingerprints.go'),
)

_FILTER = '''\tfor _, extension := range uConn.Extensions {
\t\tif ce, ok := extension.(*utls.SupportedCurvesExtension); ok {
\t\t\tce.Curves = common.Filter(ce.Curves, func(curveID utls.CurveID) bool {
\t\t\t\treturn curveID != utls.X25519MLKEM768
\t\t\t})
\t\t}
\t\tif ks, ok := extension.(*utls.KeyShareExtension); ok {
\t\t\tks.KeyShares = common.Filter(ks.KeyShares, func(share utls.KeyShare) bool {
\t\t\t\treturn share.Group != utls.X25519MLKEM768
\t\t\t})
\t\t}
\t}
'''

# Exact-match the old resolver, including its startup random selection. Keeping
# this in one removable module avoids maintaining a fork of utls_client.go.
_OLD_FINGERPRINTS = '''var (
\trandomFingerprint     utls.ClientHelloID
\trandomizedFingerprint utls.ClientHelloID
)

func init() {
\tmodernFingerprints := []utls.ClientHelloID{
\t\tutls.HelloChrome_Auto,
\t\tutls.HelloFirefox_Auto,
\t\tutls.HelloEdge_Auto,
\t\tutls.HelloSafari_Auto,
\t\tutls.HelloIOS_Auto,
\t}
\trandomFingerprint = modernFingerprints[rand.Intn(len(modernFingerprints))]

\tweights := utls.DefaultWeights
\tweights.TLSVersMax_Set_VersionTLS13 = 1
\tweights.FirstKeyShare_Set_CurveP256 = 0
\trandomizedFingerprint = utls.HelloRandomized
\trandomizedFingerprint.Seed, _ = utls.NewPRNGSeed()
\trandomizedFingerprint.Weights = &weights
}

func uTLSClientHelloID(name string) (utls.ClientHelloID, error) {
\tswitch name {
\tcase "chrome_psk", "chrome_psk_shuffle", "chrome_padding_psk_shuffle", "chrome_pq", "chrome_pq_psk":
\t\tfallthrough
\tcase "chrome", "":
\t\treturn utls.HelloChrome_Auto, nil
\tcase "firefox":
\t\treturn utls.HelloFirefox_Auto, nil
\tcase "edge":
\t\treturn utls.HelloEdge_Auto, nil
\tcase "safari":
\t\treturn utls.HelloSafari_Auto, nil
\tcase "360":
\t\treturn utls.Hello360_Auto, nil
\tcase "qq":
\t\treturn utls.HelloQQ_Auto, nil
\tcase "ios":
\t\treturn utls.HelloIOS_Auto, nil
\tcase "android":
\t\treturn utls.HelloAndroid_11_OkHttp, nil
\tcase "random":
\t\treturn randomFingerprint, nil
\tcase "randomized":
\t\treturn randomizedFingerprint, nil
\tdefault:
\t\treturn utls.ClientHelloID{}, E.New("unknown uTLS fingerprint: ", name)
\t}
}
'''

REALITY_PATCHES = (
    ('common/tls/reality_client.go', _FILTER,
     '\t// Preserve native uTLS key shares, matching Xray 26.10.21.\n'),
    ('common/tls/reality_client.go', '\t"github.com/sagernet/sing/common"\n', ''),
    ('common/tls/reality_client.go',
     '\tecdheKey := keyShareKeys.Ecdhe\n',
     '\tecdheKey := keyShareKeys.Ecdhe\n'
     '\tif ecdheKey == nil {\n'
     '\t\tecdheKey = keyShareKeys.MlkemEcdhe\n'
     '\t}\n'),
    ('common/tls/reality_client.go',
     '\tif options.Spoof != "" || options.SpoofMethod != "" {\n',
     '\tif strings.EqualFold(options.UTLS.Fingerprint, "unsafe") || strings.EqualFold(options.UTLS.Fingerprint, "hellogolang") {\n'
     '\t\treturn nil, E.New("unsupported REALITY fingerprint: ", options.UTLS.Fingerprint)\n'
     '\t}\n'
     '\tif options.Spoof != "" || options.SpoofMethod != "" {\n'),
    ('common/tls/utls_client.go', '\t"math/rand"\n', ''),
    ('common/tls/utls_client.go', _OLD_FINGERPRINTS,
     'func uTLSClientHelloID(name string) (utls.ClientHelloID, error) {\n'
     '\treturn androidLibBoxClientHelloID(name)\n'
     '}\n'),
)


def prepare_reality_dependencies(repo, core, env):
    """Apply the checksum-pinned root override to the disposable core module."""
    def manifest(path):
        return json.loads(subprocess.check_output(
            ['go', 'mod', 'edit', '-json'], cwd=path, env=env, text=True))

    replacements = [entry for entry in manifest(repo).get('Replace', [])
                    if entry['Old']['Path'] == UTLS_MODULE]
    if len(replacements) != 1 or replacements[0]['New']['Path'] != UTLS_MODULE:
        raise ValueError('Expected one pinned uTLS override in root go.mod')
    version = replacements[0]['New'].get('Version')
    if not version:
        raise ValueError('The uTLS override must use a fixed module version')
    upstream = manifest(core)
    required = [entry['Version'] for entry in upstream['Require']
                if entry['Path'] == UTLS_MODULE]
    if required != [UPSTREAM_UTLS_VERSION] or any(
            entry['Old']['Path'] == UTLS_MODULE for entry in upstream.get('Replace', [])):
        raise ValueError('Upstream uTLS dependency changed; re-evaluate the compatibility override')
    # Carry our reviewed checksums into staging before Go downloads the module.
    checksums = [line for line in (repo / 'go.sum').read_text().splitlines()
                 if line.startswith((f'{UTLS_MODULE} {version} ', f'{UTLS_MODULE} {version}/go.mod '))]
    if len(checksums) != 2:
        raise ValueError('Missing pinned uTLS checksums in root go.sum')
    sums = core / 'go.sum'
    existing = sums.read_text()
    sums.write_text(existing.rstrip('\n') + '\n' + ''.join(
        line + '\n' for line in checksums if line not in existing.splitlines()))
    subprocess.run(['go', 'mod', 'edit', f'-replace={UTLS_MODULE}={UTLS_MODULE}@{version}'],
                   cwd=core, env=env, check=True)
    subprocess.run(['go', 'mod', 'download', UTLS_MODULE], cwd=core, env=env, check=True)


def package_reality_notices(repo, aar):
    """Ship the attribution for the injected MPL-covered fingerprint table."""
    with zipfile.ZipFile(aar, 'a', zipfile.ZIP_DEFLATED) as package:
        for name in ('NOTICE', 'LICENSE-MPL-2.0'):
            package.write(repo / 'patches/tls' / name, 'META-INF/AndroidLibBoxLite/' + name)
