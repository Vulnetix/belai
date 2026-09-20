import { useRef, useState } from 'react';

/**
 * CopyButton copies a fixed value to the clipboard and announces the result.
 * Zero JS beyond the copy action: no analytics, no tracking. Keyboard
 * operable (it is a real button) and announces its state through a live
 * region so screen readers hear "copied" without a focus change.
 */
export default function CopyButton({
  value,
  label = 'copy',
  copiedLabel = 'copied',
}: {
  value: string;
  label?: string;
  copiedLabel?: string;
}) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      // Clipboard API is unavailable (non-secure context / older browser):
      // fall back to a hidden textarea selection.
      const ta = document.createElement('textarea');
      ta.value = value;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
    }
    setCopied(true);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 2000);
  };

  return (
    <span className="inline-flex items-center gap-2">
      <button
        type="button"
        onClick={copy}
        className="mono rounded-md border border-current px-3 py-2 text-sm font-medium transition-colors hover:bg-vx-mint hover:text-vx-ink focus-visible:outline-2 focus-visible:outline-offset-2"
        aria-label={`${label}: ${value}`}
      >
        {copied ? copiedLabel : label}
      </button>
      <span aria-live="polite" className="mono text-xs text-vx-mint-strong">
        {copied ? '✓ copied' : ''}
      </span>
    </span>
  );
}