import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'index',
    {
      type: 'category',
      label: 'Get Started',
      items: [
        'get-started/introduction',
        'get-started/quickstart',
        'get-started/installation',
      ],
    },
    {
      type: 'category',
      label: 'Core Concepts',
      items: [
        'concepts/architecture',
        'concepts/storage',
        'concepts/multi-tenancy',
      ],
    },
    {
      type: 'category',
      label: 'Platform Capabilities',
      items: [
        'guides/implementation-guides',
        'guides/terminology',
        'guides/validation',
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      items: [
        'reference/api',
        'reference/resource-types',
        'reference/search',
        'reference/configuration',
      ],
    },
    {
      type: 'category',
      label: 'Operations',
      items: [
        'operations/deployment',
        'operations/health-and-observability',
      ],
    },
    {
      type: 'category',
      label: 'Development',
      items: [
        'development/testing',
        'development/extending',
        'development/contributing',
      ],
    },
  ],
};

export default sidebars;
