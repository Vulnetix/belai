import { useState } from 'react';

export interface Shot {
  src: string;
  alt: string;
  caption?: string;
}

/**
 * ShotCarousel shows one TUI capture at a time with prev/next controls and a
 * dot strip. The frames are real rendered TUI surfaces (see tools/shot);
 * the carousel adds only the interaction needed to page through them.
 */
export default function ShotCarousel({ shots }: { shots: Shot[] }) {
  const [index, setIndex] = useState(0);
  if (shots.length === 0) return null;
  const current = shots[Math.min(index, shots.length - 1)];

  const go = (next: number) => setIndex((next + shots.length) % shots.length);

  return (
    <figure className="m-0">
      <div className="overflow-hidden rounded-lg border border-vx-ink bg-vx-ink">
        <img
          src={current.src}
          alt={current.alt}
          className="block h-auto w-full"
          loading="lazy"
        />
      </div>
      <figcaption className="mt-3 flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => go(index - 1)}
            aria-label="Previous capture"
            className="mono rounded-md border border-vx-ink/40 px-2.5 py-1 text-sm hover:border-vx-ink"
          >
            ←
          </button>
          <span className="mono text-sm text-vx-ink">
            {index + 1} / {shots.length}
          </span>
          <button
            type="button"
            onClick={() => go(index + 1)}
            aria-label="Next capture"
            className="mono rounded-md border border-vx-ink/40 px-2.5 py-1 text-sm hover:border-vx-ink"
          >
            →
          </button>
        </div>
        <div className="flex flex-wrap gap-1.5" role="tablist" aria-label="Capture dots">
          {shots.map((s, i) => (
            <button
              key={s.src}
              type="button"
              role="tab"
              aria-selected={i === index}
              aria-label={`Show capture ${i + 1}: ${s.alt}`}
              onClick={() => go(i)}
              className={`h-2.5 w-2.5 rounded-full border ${
                i === index ? 'border-vx-ink bg-vx-ink' : 'border-vx-ink/40 bg-transparent'
              }`}
            />
          ))}
        </div>
      </figcaption>
      {current.caption ? (
        <p className="mono mt-2 text-xs text-vx-ink/60">{current.caption}</p>
      ) : null}
    </figure>
  );
}