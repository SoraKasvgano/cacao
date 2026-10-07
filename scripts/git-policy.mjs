import { execFileSync } from 'node:child_process';
import { appendFileSync, readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const subjectPattern = /^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9][a-z0-9._/-]*\))?!?: \S.*$/u;
const generatedPath = /(^|\/)(package-lock\.json|yarn\.lock|pnpm-lock\.yaml|go\.sum)$|^frontend\/dist\//;

export function validSubject(subject) {
  return subject === subject.trim() && !/[\r\n]/u.test(subject) &&
    [...subject].length <= 100 && subjectPattern.test(subject);
}

function git(args, cwd) {
  return execFileSync('git', args, { cwd, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
}

export function inspectRange(base, head, cwd = process.cwd()) {
  const resolve = (ref) => git(['rev-parse', '--verify', '--end-of-options', `${ref}^{commit}`], cwd).trim();
  const baseSha = resolve(base);
  const headSha = resolve(head);
  const mergeBase = git(['merge-base', baseSha, headSha], cwd).trim();
  const records = git(['log', '-z', '--format=%H%x00%P%x00%s', `${baseSha}..${headSha}`], cwd).split('\0');
  const commits = [];
  for (let index = 0; index + 2 < records.length; index += 3) {
    commits.push({ sha: records[index], merge: records[index + 1].split(' ').length > 1, subject: records[index + 2] });
  }

  const stats = { files: 0, lines: 0, reviewLines: 0, generatedFiles: 0, binaryFiles: 0 };
  const entries = git(['diff', '--numstat', '-z', '--find-renames', `${baseSha}...${headSha}`, '--'], cwd).split('\0');
  for (let index = 0; index < entries.length && entries[index]; index++) {
    const [added, removed, ...name] = entries[index].split('\t');
    let path = name.join('\t');
    if (path === '') {
      index++; // A rename has separate old and new NUL-terminated paths.
      path = entries[++index];
    }
    const lines = added === '-' ? 0 : Number(added) + Number(removed);
    const generated = generatedPath.test(path);
    stats.files++;
    stats.lines += lines;
    stats.reviewLines += generated ? 0 : lines;
    stats.generatedFiles += Number(generated);
    stats.binaryFiles += Number(added === '-');
  }
  return { mergeBase, commits, stats };
}

export function checkPullRequest(base, head, title, cwd = process.cwd()) {
  const result = inspectRange(base, head, cwd);
  const errors = [];
  if (!validSubject(title)) errors.push('PR title must use type(scope): description, with at most 100 characters.');
  for (const commit of result.commits) {
    if (!commit.merge && !validSubject(commit.subject)) {
      errors.push(`Commit ${commit.sha.slice(0, 12)} must use a Conventional Commit subject (at most 100 characters).`);
    }
  }
  const { stats } = result;
  const warnings = [];
  if (stats.reviewLines > 400 || stats.files > 15) {
    warnings.push('PR exceeds the suggested 400 non-generated changed lines or 15 files. Explain cohesion and review order, or split independent goals.');
  }
  return { ...result, errors, warnings };
}

function main() {
  const [mode, ...args] = process.argv.slice(2);
  if (mode === 'commit-msg') {
    const message = execFileSync('git', ['stripspace', '--strip-comments'], {
      input: readFileSync(args[0], 'utf8'), encoding: 'utf8',
    });
    const subject = message.split(/\r?\n/u)[0];
    // Git produces these subjects when merging; CI identifies merges by parent count.
    if (subject.startsWith('Merge ') || validSubject(subject)) return;
    throw new Error('Use type(scope): description (at most 100 characters); see CONTRIBUTING.md.');
  }
  if (mode !== 'pr') throw new Error('Usage: node scripts/git-policy.mjs commit-msg FILE | pr [BASE HEAD TITLE]');
  let [base, head, title] = args;
  if (args.length === 0) {
    const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
    ({ base: { sha: base }, head: { sha: head }, title } = event.pull_request);
  } else if (args.length !== 3) {
    throw new Error('Local PR checks require BASE HEAD TITLE.');
  }
  const { commits, stats, errors, warnings } = checkPullRequest(base, head, title);
  const summary = `PR scope: ${commits.length} commits, ${stats.files} files, ${stats.lines} total changed lines; ${stats.reviewLines} non-generated lines, ${stats.generatedFiles} generated/lock files, ${stats.binaryFiles} binary files.`;
  console.log(summary);
  for (const warning of warnings) console.warn(`WARNING: ${warning}`);
  for (const error of errors) console.error(`ERROR: ${error}`);
  if (process.env.GITHUB_ACTIONS === 'true') {
    for (const warning of warnings) console.log(`::warning::${warning}`);
    for (const error of errors) console.log(`::error::${error}`);
    if (process.env.GITHUB_STEP_SUMMARY) {
      appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${summary}\n\n${[...warnings, ...errors].join('\n\n')}\n`);
    }
  }
  if (errors.length) process.exitCode = 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try { main(); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
