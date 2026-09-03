import React from 'react';
import Content from '@theme-original/DocItem/Content';
import type ContentType from '@theme/DocItem/Content';
import type {WrapperProps} from '@docusaurus/types';
import PageActions from '@site/src/components/PageActions';

type Props = WrapperProps<typeof ContentType>;

// Adds the page-actions row (Copy page / View as Markdown / Open in an LLM)
// above the rendered Markdown of every doc page.
export default function ContentWrapper(props: Props): React.ReactNode {
  return (
    <>
      <PageActions />
      <Content {...props} />
    </>
  );
}
