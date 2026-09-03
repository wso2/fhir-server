import React, {useEffect, useRef, useState} from 'react';
import {useDoc} from '@docusaurus/plugin-content-docs/client';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import useBaseUrl from '@docusaurus/useBaseUrl';
import styles from './styles.module.css';

// The doc's Markdown source is copied to static/md/ at build time
// (scripts/copy-docs-markdown.mjs), so every page has a retrievable source
// file at the same relative path it has under docs/.
function sourceToMdPath(source: string): string {
  return source.replace(/^@site\/docs\//, '');
}

type CopyState = 'idle' | 'copied' | 'failed';

export default function PageActions(): React.ReactNode {
  const {metadata} = useDoc();
  const {siteConfig} = useDocusaurusContext();
  const [copyState, setCopyState] = useState<CopyState>('idle');
  const [open, setOpen] = useState(false);
  const wrapperRef = useRef<HTMLDivElement>(null);

  const mdPath = sourceToMdPath(metadata.source);
  const mdUrl = useBaseUrl(`md/${mdPath}`);
  const absoluteMdUrl = `${siteConfig.url.replace(/\/$/, '')}${mdUrl}`;

  const prompt = `Read ${absoluteMdUrl} — it is the Markdown source of the WSO2 FHIR Server documentation page "${metadata.title}". Answer my questions about it.`;
  const encoded = encodeURIComponent(prompt);

  // Close on a click anywhere outside the menu, or on Escape. Deliberately not
  // on mouseleave: the menu is offset below its button, and closing on leave
  // makes the items impossible to click.
  useEffect(() => {
    if (!open) {
      return undefined;
    }
    function onPointerDown(event: MouseEvent | TouchEvent) {
      if (!wrapperRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    }
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  async function copyPage() {
    try {
      const res = await fetch(mdUrl);
      if (!res.ok) {
        throw new Error(String(res.status));
      }
      await navigator.clipboard.writeText(await res.text());
      setCopyState('copied');
    } catch {
      setCopyState('failed');
    }
    setTimeout(() => setCopyState('idle'), 2000);
  }

  const copyLabel =
    copyState === 'copied'
      ? 'Copied'
      : copyState === 'failed'
        ? 'Copy failed'
        : 'Copy page';

  return (
    <div className={styles.actions}>
      <button
        type="button"
        className={styles.copyButton}
        onClick={copyPage}
        aria-live="polite">
        <span aria-hidden="true" className={styles.icon}>
          {copyState === 'copied' ? '✓' : '⧉'}
        </span>
        {copyLabel}
      </button>

      <div className={styles.menuWrapper} ref={wrapperRef}>
        <button
          type="button"
          className={styles.menuButton}
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label="More page actions"
          onClick={() => setOpen((v) => !v)}>
          <span aria-hidden="true">▾</span>
        </button>

        {open && (
          <ul className={styles.menu} role="menu">
            <li role="none">
              <a
                role="menuitem"
                href={mdUrl}
                target="_blank"
                rel="noopener noreferrer"
                onClick={() => setOpen(false)}>
                View as Markdown
              </a>
            </li>
            <li role="none">
              <a
                role="menuitem"
                href={`https://chatgpt.com/?q=${encoded}`}
                target="_blank"
                rel="noopener noreferrer"
                onClick={() => setOpen(false)}>
                Open in ChatGPT
              </a>
            </li>
            <li role="none">
              <a
                role="menuitem"
                href={`https://claude.ai/new?q=${encoded}`}
                target="_blank"
                rel="noopener noreferrer"
                onClick={() => setOpen(false)}>
                Open in Claude
              </a>
            </li>
          </ul>
        )}
      </div>
    </div>
  );
}
