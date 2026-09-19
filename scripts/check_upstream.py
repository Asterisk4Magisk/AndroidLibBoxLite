"""Track the latest reF1nd sing-box stable or preview tag."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.error
import urllib.request

MODULE = 'github.com/sagernet/sing-box'
FORK = 'github.com/reF1nd/sing-box'
UPSTREAM = 'reF1nd/sing-box'
TAG = re.compile(r'v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?-reF1nd')


def version(tag):
    match = TAG.fullmatch(tag)
    if not match:
        raise ValueError(f'Not a reF1nd version tag: {tag!r}')
    preview = match[4]
    identifiers = []
    for part in preview.split('.') if preview else []:
        if part.isdigit():
            if len(part) > 1 and part[0] == '0':
                raise ValueError(f'Noncanonical version tag: {tag!r}')
            identifiers.append((0, int(part)))
        else:
            identifiers.append((1, part))
    return (*map(int, match.groups()[:3]), preview is None, tuple(identifiers))


def select_latest(tags):
    candidates = []
    for tag in tags:
        try:
            candidates.append((version(tag), tag))
        except ValueError:
            pass
    if not candidates:
        raise ValueError('Upstream has no versioned reF1nd tags')
    return max(candidates)[1]


def pinned(repo):
    return pinned_text((repo / 'go.mod').read_text())


def pinned_text(text):
    core = re.search(r'(?m)^\s*' + re.escape(MODULE) + r'\s+(\S+)', text).group(1)
    fork = re.search(r'(?m)^replace ' + re.escape(MODULE) + r' => ' + re.escape(FORK) + r' (\S+)', text).group(1)
    if core != fork:
        raise ValueError('Core requirement and fork replacement must pin the same tag')
    version(core)
    return core


def plan_update(current, latest, published):
    if version(latest) < version(current):
        return dict(update=False, build=False, tag='')
    update = version(latest) > version(current)
    if update and published:
        raise ValueError('Latest release is published but go.mod is older; reconcile manually')
    return dict(update=update, build=not published, tag=latest if not published else '')


def github(path, missing_ok=False):
    headers = {'Accept': 'application/vnd.github+json', 'User-Agent': 'AndroidLibBoxLite'}
    token = os.environ.get('GH_TOKEN') or os.environ.get('GITHUB_TOKEN')
    if token:
        headers['Authorization'] = 'Bearer ' + token
    request = urllib.request.Request('https://api.github.com/' + path, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if missing_ok and error.code == 404:
            return None
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', help='Destination owner/repo')
    parser.add_argument('--apply', action='store_true', help='Update the pinned core and checksums')
    parser.add_argument('--tag', help='Use an already selected upstream tag')
    parser.add_argument('--output', help='Append GitHub Actions outputs')
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    current = pinned(repo)
    if args.tag:
        version(args.tag)
        latest = args.tag
    else:
        tags = []
        page = 1
        while True:
            batch = github(f'repos/{UPSTREAM}/tags?per_page=100&page={page}')
            tags.extend(t['name'] for t in batch)
            if len(batch) < 100:
                break
            page += 1
        latest = select_latest(tags)
    release = github(f'repos/{args.repository}/releases/tags/{latest}', missing_ok=True) if args.repository else None
    published = bool(release and not release['draft'])
    plan = plan_update(current, latest, published)
    print(json.dumps(dict(current=current, latest=latest, **plan)))
    if args.apply and plan['update']:
        subprocess.run(['go', 'mod', 'edit', f'-require={MODULE}@{latest}',
                        f'-replace={MODULE}={FORK}@{latest}'], cwd=repo, check=True)
        downloaded = json.loads(subprocess.check_output(
            ['go', 'mod', 'download', '-json', MODULE], cwd=repo, text=True))
        upstream_go = re.search(r'(?m)^go (\S+)', Path(downloaded['GoMod']).read_text()).group(1)
        local_go = re.search(r'(?m)^go (\S+)', (repo / 'go.mod').read_text()).group(1)
        if tuple(map(int, upstream_go.split('.'))) > tuple(map(int, local_go.split('.'))):
            subprocess.run(['go', 'mod', 'edit', '-go=' + upstream_go], cwd=repo, check=True)
    if args.output:
        with open(args.output, 'a', encoding='utf-8') as output:
            for key, value in plan.items():
                output.write(f'{key}={str(value).lower() if isinstance(value, bool) else value}\n')


if __name__ == '__main__':
    main()
