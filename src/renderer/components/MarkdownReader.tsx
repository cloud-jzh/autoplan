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

function toVisibleHtmlComment(comment: string) {
  if (!comment.includes('`')) return `\`${comment}\``;
  return `\`\` ${comment} \`\``;
}

function exposeHtmlComments(markdown: string) {
  let fence: { char: string; length: number } | null = null;

  return markdown
    .split('\n')
    .map((line) => {
      const fenceMatch = line.match(/^ {0,3}(`{3,}|~{3,})/);

      if (fence) {
        if (fenceMatch && fenceMatch[1].startsWith(fence.char) && fenceMatch[1].length >= fence.length) {
          fence = null;
        }
        return line;
      }

      const visibleLine = line.replace(/<!--.*?-->/g, toVisibleHtmlComment);

      if (fenceMatch) {
        fence = { char: fenceMatch[1][0], length: fenceMatch[1].length };
      }

      return visibleLine;
    })
    .join('\n');
}

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
  input({ node: _node, type, checked, ...props }) {
    if (type !== 'checkbox') return <input {...props} type={type} />;

    return (
      <input
        {...props}
        type="checkbox"
        checked={Boolean(checked)}
        readOnly
        disabled
        aria-label={checked ? '已勾选任务' : '未勾选任务'}
      />
    );
  },
  pre({ node: _node, ...props }) {
    return <pre {...props} tabIndex={0} aria-label="可滚动代码块" />;
  },
  table({ node: _node, ...props }) {
    return (
      <div className="markdown-reader-table" role="region" aria-label="可横向滚动表格" tabIndex={0}>
        <table {...props} />
      </div>
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
      <div className={`${classes} markdown-reader-empty`} role="status" aria-live="polite">
        {emptyMessage}
      </div>
    );
  }

  return (
    <div className={classes} role="region" aria-label={ariaLabel}>
      <ReactMarkdown
        components={markdownComponents}
        remarkPlugins={[remarkGfm]}
        urlTransform={safeUrlTransform}
      >
        {exposeHtmlComments(content)}
      </ReactMarkdown>
    </div>
  );
}

export default MarkdownReader;
