import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import {themes as prismThemes} from 'prism-react-renderer';

const config: Config = {
  title: 'WSO2 FHIR Server',
  tagline: 'A production-oriented FHIR R4 REST server built in Go and backed by PostgreSQL',
  url: 'https://wso2.github.io',
  baseUrl: '/fhir-server/',
  organizationName: 'wso2',
  projectName: 'fhir-server',
  onBrokenLinks: 'throw',
  markdown: {mermaid: true, hooks: {onBrokenMarkdownLinks: 'throw'}},
  themes: [
    '@docusaurus/theme-mermaid',
    [
      '@easyops-cn/docusaurus-search-local',
      {
        // Offline search: the index is built at `docusaurus build` time and
        // shipped with the site, so there is no external search service.
        hashed: true,
        language: ['en'],
        indexBlog: false,
        indexPages: false,
        // Docs are served at /fhir-server/docs on GitHub Pages.
        docsRouteBasePath: '/docs',
        highlightSearchTermsOnTargetPage: true,
        explicitSearchResultPath: true,
        searchBarShortcutHint: false,
      },
    ],
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          path: 'docs',
          routeBasePath: '/docs',
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/wso2/fhir-server/edit/main/website/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    mermaid: {
      // Follow the site's light/dark mode so the diagram stays legible in both.
      theme: {light: 'neutral', dark: 'dark'},
    },
    navbar: {
      title: 'FHIR Server',
      logo: {
        alt: 'WSO2',
        src: 'img/logo.svg',
        srcDark: 'img/logo-dark.svg',
        href: '/docs/',
      },
      items: [
        {
          href: 'https://hl7.org/fhir/R4/',
          label: 'FHIR R4',
          position: 'right',
        },
        {
          href: 'https://github.com/wso2/fhir-server',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [],
      copyright: `Copyright © ${new Date().getFullYear()} WSO2 LLC.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'yaml'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
