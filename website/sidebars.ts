import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'index',
    {
      type: 'category',
      label: 'Get Started',
      items: [
        'get-started/quickstart',
        'get-started/installation',
      ],
    },
    {
      type: 'category',
      label: 'Architecture',
      items: [
        'architecture/architecture',
        'architecture/storage',
      ],
    },
    {
      type: 'category',
      label: 'FHIR API',
      items: [
        'api/interactions',
        {
          type: 'category',
          label: 'Search',
          link: {type: 'doc', id: 'api/search'},
          items: [
            'api/search-joins',
            'api/search-results',
          ],
        },
        {
          type: 'category',
          label: 'Operations',
          link: {type: 'doc', id: 'api/operations'},
          items: [
            'api/conditional',
          ],
        },
        'api/capability-statement',
      ],
    },
    {
      type: 'category',
      label: 'Profiles & Conformance',
      items: [
        'conformance/implementation-guides',
        'conformance/validation',
        'conformance/terminology',
        'conformance/resource-types',
      ],
    },
    {
      type: 'category',
      label: 'Administration',
      items: [
        'administration/deployment',
        'administration/configuration',
        'administration/multi-tenancy',
        'administration/health-checks',
        'administration/observability',
      ],
    },
    {
      type: 'category',
      label: 'Contributing',
      link: {type: 'doc', id: 'contributing/contributing'},
      items: [
        'contributing/testing',
      ],
    },
  ],
};

export default sidebars;
