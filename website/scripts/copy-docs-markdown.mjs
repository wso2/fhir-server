// Copies every doc source file into static/md/ so the page-actions widget can
// offer "Copy page" and "View as Markdown" against the real Markdown source.
//
// Runs before `docusaurus start` and `docusaurus build` (see package.json), so
// the files are available identically in development and in a production build.
// The generated directory is disposable and git-ignored.

import {cp, mkdir, readdir, rm} from 'node:fs/promises';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

const websiteDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const src = resolve(websiteDir, 'docs');
const dest = resolve(websiteDir, 'static/md');
const versionedDocsDir = resolve(websiteDir, 'versioned_docs');
const filter = (from) => !from.endsWith('.DS_Store');

await rm(dest, {recursive: true, force: true});
await mkdir(dest, {recursive: true});
await cp(src, dest, {recursive: true, filter});

const versions = await readdir(versionedDocsDir, {withFileTypes: true}).catch(() => []);
for (const version of versions) {
  if (!version.isDirectory() || !version.name.startsWith('version-')) {
    continue;
  }
  await cp(resolve(versionedDocsDir, version.name), resolve(dest, version.name), {
    recursive: true,
    filter,
  });
}

console.log(`[copy-docs-markdown] docs/ and versioned_docs/ -> static/md/`);
