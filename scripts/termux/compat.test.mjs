import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const tmpRoot = process.env.REASONIX_TMPDIR || join(homedir(), 'tmp');
mkdirSync(tmpRoot, { recursive: true });
function fixture(t) {
  const dir = mkdtempSync(join(tmpRoot, 'reasonix-compat-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}
function run(command, args, cwd, env = {}) {
  return spawnSync(command, args, {
    cwd, env: { ...process.env, ...env }, encoding: 'utf8', timeout: 30000,
  });
}
function git(dir, ...args) {
  const result = run('git', ['-c', 'user.name=fixture', '-c', 'user.email=fixture@invalid', ...args], dir);
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

// A recorder catches argv ordering without creating an agent session.
test('wrapper preserves command dispatch and explicit permission choices', t => {
  const dir = fixture(t);
  const real = join(dir, 'real');
  writeFileSync(real, `#!${process.execPath}\nprocess.stdout.write(JSON.stringify(process.argv.slice(2)));\n`);
  chmodSync(real, 0o755);
  const mode = ['--permission-mode', 'danger-full-access'];
  const cases = [
    [[], mode],
    [['run', 'hello world'], ['run', ...mode, 'hello world']],
    [['chat', '--resume'], ['chat', ...mode, '--resume']],
    [['code'], ['code', ...mode]],
    [['-p', 'hello'], [...mode, '-p', 'hello']],
    [['run', '--', '--permission-mode'], ['run', ...mode, '--', '--permission-mode']],
    [['run', '--permission-mode=read-only', 'hello'], ['run', '--permission-mode=read-only', 'hello']],
    [['run', '--auto', 'hello'], ['run', '--auto', 'hello']],
    [['run', '-y', 'hello'], ['run', '-y', 'hello']],
  ];
  for (const name of ['remote', 'plugin', 'session', 'sessions', 'catalogs', 'update', 'hooks', 'review', 'docs-manifest', '--acp', '--help', '--version', 'future-command']) {
    cases.push([[name], [name]]);
  }
  for (const [args, expected] of cases) {
    const result = run('bash', [join(root, 'scripts/termux/reasonix-wrapper'), ...args], dir, { REASONIX_REAL: real, REASONIX_FULL_ACCESS: '1', REASONIX_PERMISSION_MODE: 'danger-full-access' });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), expected, `args ${JSON.stringify(args)}`);
  }
  const optOut = run('bash', [join(root, 'scripts/termux/reasonix-wrapper'), 'run', 'hello'], dir, { REASONIX_REAL: real, REASONIX_FULL_ACCESS: '0' });
  assert.deepEqual(JSON.parse(optOut.stdout), ['run', 'hello']);
});

function updateFixture(t) {
  const dir = fixture(t);
  const script = readFileSync(join(root, 'update-reasonix.sh'), 'utf8');
  const names = new Set(['update-reasonix.sh', 'reasonix-termux.patch', 'release-notes/releases.json', '.gitignore', '.gitattributes']);
  for (const match of script.matchAll(/"(internal\/[^":]+):/g)) names.add(match[1]);
  // Script sentinels and workflow contracts live outside the cumulative patch.
  names.add('scripts/termux/reasonix-wrapper');
  names.add('scripts/termux/compat.test.mjs');
  names.add('.github/workflows/build_termux.yml');
  names.add('.github/workflows/verify-and-release.yml');
  names.add('release-termux.sh');
  for (const name of names) {
    mkdirSync(dirname(join(dir, name)), { recursive: true });
    copyFileSync(join(root, name), join(dir, name));
  }
  writeFileSync(join(dir, 'tracked.txt'), 'original user content\n');
  git(dir, 'init', '-q');
  git(dir, 'add', '.');
  git(dir, 'commit', '-qm', 'fixture');
  return dir;
}

function updateWithShim(t, dir, body) {
  const shim = fixture(t);
  const trace = join(shim, 'trace');
  const realGit = run('bash', ['-c', 'command -v git'], dir).stdout.trim();
  const realBash = run('bash', ['-c', 'command -v bash'], dir).stdout.trim();
  writeFileSync(join(shim, 'git'), `#!${realBash}\nprintf '%s\\n' "$*" >> "$TRACE"\n${body}\nexec "${realGit}" "$@"\n`);
  chmodSync(join(shim, 'git'), 0o755);
  const result = run('bash', [join(dir, 'update-reasonix.sh')], dir, {
    PATH: `${shim}:${process.env.PATH}`, TRACE: trace, KEEP_CHANGES: '0',
    REASONIX_TAG: '', REASONIX_REPO_URL: 'https://invalid.example/upstream',
    TERMUX_PATCH_FILE: join(dir, 'reasonix-termux.patch'), TMPDIR: tmpRoot,
  });
  const calls = existsSync(trace) ? readFileSync(trace, 'utf8') : '';
  return { result, calls };
}

test('update refuses tracked and untracked edits before network or rollback', t => {
  for (const staged of [false, true]) {
    const dir = updateFixture(t);
    writeFileSync(join(dir, 'tracked.txt'), 'unsaved edit\n');
    writeFileSync(join(dir, 'untracked.txt'), 'unsaved data\n');
    if (staged) git(dir, 'add', 'tracked.txt');
    const { result, calls } = updateWithShim(t, dir, '');
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /拒绝升级/);
    assert.doesNotMatch(calls, /ls-remote|reset|clean|^rm /m);
    assert.equal(readFileSync(join(dir, 'tracked.txt'), 'utf8'), 'unsaved edit\n');
    assert.equal(readFileSync(join(dir, 'untracked.txt'), 'utf8'), 'unsaved data\n');
  }
});

test('update network failure leaves ignored data and HEAD untouched', t => {
  const dir = updateFixture(t);
  const before = git(dir, 'rev-parse', 'HEAD');
  mkdirSync(join(dir, 'bin'));
  writeFileSync(join(dir, 'bin/user-data'), 'ignored output\n');
  const { result, calls } = updateWithShim(t, dir, 'if [ "${1-}" = ls-remote ]; then exit 128; fi');
  assert.notEqual(result.status, 0);
  assert.match(calls, /ls-remote/, result.stderr);
  assert.doesNotMatch(calls, /reset|clean|^rm /m);
  assert.equal(git(dir, 'rev-parse', 'HEAD'), before);
  assert.equal(readFileSync(join(dir, 'bin/user-data'), 'utf8'), 'ignored output\n');
});

test('update checkout failure rolls back only tracked source paths', t => {
  const dir = updateFixture(t);
  const before = git(dir, 'rev-parse', 'HEAD');
  mkdirSync(join(dir, 'bin'));
  writeFileSync(join(dir, 'bin/user-data'), 'ignored output\n');
  const body = `case "${'${1-}'}" in
    ls-remote) printf '1234567890123456789012345678901234567890\\trefs/tags/v1.99.0\\n'; exit 0;;
    fetch) exit 0;;
    checkout) printf 'late user data\\n' > late-untracked.txt; exit 1;;
  esac`;
  const { result, calls } = updateWithShim(t, dir, body);
  assert.notEqual(result.status, 0);
  assert.match(calls, /^reset --hard /m, result.stderr);
  assert.doesNotMatch(calls, /\bclean\b/);
  assert.equal(git(dir, 'rev-parse', 'HEAD'), before);
  assert.equal(readFileSync(join(dir, 'tracked.txt'), 'utf8'), 'original user content\n');
  assert.equal(readFileSync(join(dir, 'late-untracked.txt'), 'utf8'), 'late user data\n');
  assert.equal(readFileSync(join(dir, 'bin/user-data'), 'utf8'), 'ignored output\n');
});

test('release rejects version mismatches and requires an existing tag', () => {
  const release = readFileSync(join(root, 'release-termux.sh'), 'utf8');
  assert.ok(release.includes('[ "$BIN_BASE" = "${TAG#v}" ] ||'));
  assert.ok(release.indexOf('产物版本') < release.indexOf('git add "artifacts/'));
  const workflow = readFileSync(join(root, '.github/workflows/verify-and-release.yml'), 'utf8');
  assert.ok(workflow.includes('--verify-tag'));
});

test('Termux workflow builds the exact repository revision', () => {
  const workflow = readFileSync(join(root, '.github/workflows/build_termux.yml'), 'utf8');
  assert.ok(workflow.includes('https://codeload.github.com/${{ github.repository }}/tar.gz/${{ github.sha }}'));
  assert.ok(workflow.includes('VERSION="$TERMUX_PKG_VERSION-termux.${revision:0:8}"'));
  assert.doesNotMatch(workflow, /lengxiaohua123|refs\/heads\/master/);
});
