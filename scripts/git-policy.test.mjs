import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { checkPullRequest, validSubject } from './git-policy.mjs';

test('Conventional subjects support Chinese, scopes and breaking changes', () => {
  assert.equal(validSubject('fix(auth): 过期会话返回未登录状态'), true);
  assert.equal(validSubject('feat(api)!: remove legacy login'), true);
  for (const subject of ['misc changes', 'fix: ', 'fixup! fix: login', 'fix: x\nci: y', `fix: ${'x'.repeat(96)}`]) {
    assert.equal(validSubject(subject), false, subject);
  }
});

test('real PR ranges exclude advancing base and parent-PR commits; size only warns', (t) => {
  const fixtureRoot = realpathSync(tmpdir());
  const cwd = mkdtempSync(join(fixtureRoot, 'cacao-policy-'));
  t.after(() => {
    assert.equal(dirname(realpathSync(cwd)), fixtureRoot);
    assert.match(basename(cwd), /^cacao-policy-/u);
    rmSync(cwd, { recursive: true, force: true });
  });
  const git = (...args) => execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  git('init', '-b', 'main');
  git('config', 'user.name', 'Policy Test');
  git('config', 'user.email', 'policy@example.invalid');
  git('config', 'core.hooksPath', '.unused-hooks');
  git('config', 'commit.gpgSign', 'false');
  git('config', 'core.autocrlf', 'false');
  const commit = (path, contents, subject) => {
    writeFileSync(join(cwd, path), contents);
    git('add', '--', path);
    git('commit', '-m', subject);
    return git('rev-parse', 'HEAD');
  };
  const common = commit('base.txt', 'base\n', 'legacy title predating policy');
  git('switch', '-c', 'feature');
  commit('feature.txt', 'feature\n', 'feat: add feature');
  git('switch', 'main');
  commit('base-only.txt', 'base only\n'.repeat(600), 'legacy base title');
  let result = checkPullRequest('main', 'feature', 'feat: add feature', cwd);
  assert.equal(result.mergeBase, common);
  assert.equal(result.commits.length, 1);
  assert.equal(result.stats.reviewLines, 1);
  assert.deepEqual(result.errors, []);
  assert.deepEqual(result.warnings, []);

  git('switch', 'feature');
  git('merge', '--no-ff', 'main', '-m', 'Merge main into feature');
  result = checkPullRequest('main', 'feature', 'feat: add feature', cwd);
  assert.equal(result.commits.length, 2);
  assert.deepEqual(result.errors, []);
  git('switch', '-c', 'child');
  commit('large.txt', 'line\n'.repeat(401), 'feat: add dependent feature');
  mkdirSync(join(cwd, 'frontend'));
  commit('frontend/package-lock.json', '{}\n'.repeat(500), 'build: lock dependent feature');
  git('mv', 'feature.txt', 'renamed feature.txt');
  git('commit', '-m', 'refactor: rename feature file');
  result = checkPullRequest('feature', 'child', 'feat: add dependent feature', cwd);
  assert.equal(result.commits.length, 3);
  assert.equal(result.stats.files, 3);
  assert.equal(result.stats.reviewLines, 401);
  assert.equal(result.stats.lines, 901);
  assert.equal(result.stats.generatedFiles, 1);
  assert.equal(result.warnings.length, 1);
  assert.deepEqual(result.errors, []);
  const eventPath = join(cwd, 'event.json');
  writeFileSync(eventPath, JSON.stringify({ pull_request: {
    base: { sha: git('rev-parse', 'feature') }, head: { sha: git('rev-parse', 'child') },
    title: 'feat: add dependent feature',
  } }));
  const output = execFileSync(process.execPath, [fileURLToPath(new URL('./git-policy.mjs', import.meta.url)), 'pr'], {
    cwd, encoding: 'utf8', env: { ...process.env, GITHUB_ACTIONS: 'false', GITHUB_EVENT_PATH: eventPath },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  assert.match(output, /401 non-generated lines/u);
  commit('bad.txt', 'bad\n', 'unrelated stuff');
  result = checkPullRequest('feature', 'child', 'bad title', cwd);
  assert.equal(result.errors.length, 2);

  mkdirSync(join(cwd, 'scripts'));
  mkdirSync(join(cwd, '.githooks'));
  writeFileSync(join(cwd, 'scripts/git-policy.mjs'), readFileSync(new URL('./git-policy.mjs', import.meta.url)));
  writeFileSync(join(cwd, '.githooks/commit-msg'), readFileSync(new URL('../.githooks/commit-msg', import.meta.url)), { mode: 0o755 });
  git('config', 'core.hooksPath', '.githooks');
  assert.throws(() => commit('hook.txt', 'hook\n', 'bad local title'), /Use type\(scope\)/u);
  git('commit', '-m', 'test: verify local hook');
  assert.equal(git('log', '-1', '--format=%s'), 'test: verify local hook');
  writeFileSync(join(cwd, 'hook.txt'), 'updated hook\n');
  git('add', 'hook.txt');
  git('config', 'core.commentChar', ';');
  writeFileSync(join(cwd, 'message.txt'), '; editor template comment\n\nfix: accept cleaned message\n');
  git('commit', '--cleanup=strip', '-F', 'message.txt');
  assert.equal(git('log', '-1', '--format=%s'), 'fix: accept cleaned message');
});
