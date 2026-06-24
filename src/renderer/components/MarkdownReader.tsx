import type { ComponentProps } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

type ReactMarkdownProps = ComponentProps<typeof ReactMarkdown>;
type MarkdownComponents = NonNullable<ReactMarkdownProps['components']>;
type UrlTransform = NonNullable<ReactMarkdownProps['urlTransform']>;

export type MarkdownReaderProps = {
  markdown?: string | null;
  className?: string;
  emptyMessage?: string;
  ariaLabel?: string;
};

const allowedUrlProtocols = new Set(['http:', 'https:', 'mailto:', 'tel:']);

function normalizeSafeUrl(url: string): string | undefined {
  const trimmedUrl = url.trim();

  if (!trimmedUrl || trimmedUrl.startsWith('//')) {
    return undefined;
  }

  if (/^[a-zA-Z][a-zA-Z\d+.-]*:/.test(trimmedUrl)) {
    try {
      const parsedUrl = new URL(trimmedUrl);
      return allowedUrlProtocols.has(parsedUrl.protocol) ? trimmedUrl : undefined;
    } catch {
      return undefined;
    }
  }

  return trimmedUrl;
}

const safeUrlTransform: UrlTransform = (url) => normalizeSafeUrl(url) ?? '';

const markdownComponents: MarkdownComponents = {
  a({ node: _node, href, children, ...props }) {
    const safeHref = href ? normalizeSafeUrl(href) : undefined;
    const isExternalLink = Boolean(safeHref && /^https?:\/\//i.test(safeHref));

    return (
      <a
        {...props}
        href={safeHref}
        target={isExternalLink ? '_blank' : undefined}
        rel={isExternalLink ? 'noreferrer noopener' : undefined}
      >
        {children}
      </a>
    );
  },
};

export function MarkdownReader({
  markdown,
  className,
  emptyMessage = '暂无计划正文',
  ariaLabel = 'Markdown 正文',
}: MarkdownReaderProps) {
  const content = markdown ?? '';
  const classes = ['markdown-reader', className].filter(Boolean).join(' ');

  if (!content.trim()) {
    return (
      <div className={`${classes} markdown-reader-empty`} role="status">
        {emptyMessage}
      </div>
    );
  }

  return (
    <div className={classes} aria-label={ariaLabel}>
      <ReactMarkdown
        components={markdownComponents}
        remarkPlugins={[remarkGfm]}
        urlTransform={safeUrlTransform}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}

export default MarkdownReader;
