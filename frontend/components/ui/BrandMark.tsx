/** The product mark: three connected nodes. Decorative. */
export function BrandMark({ size = 24 }: { size?: number }) {
  return (
    <svg className="brand-mark" width={size} height={size} viewBox="0 0 24 24" aria-hidden focusable="false">
      <rect x="1" y="2" width="8" height="6" rx="2" fill="currentColor" opacity="0.55" />
      <rect x="1" y="16" width="8" height="6" rx="2" fill="currentColor" opacity="0.55" />
      <rect x="15" y="9" width="8" height="6" rx="2" fill="currentColor" />
      <path d="M9 5c4 0 3 7 6 7M9 19c4 0 3-7 6-7" fill="none" stroke="currentColor" strokeWidth="1.6" />
    </svg>
  );
}

export function Spinner({ label }: { label?: string }) {
  return <span className="spinner" role={label ? "status" : undefined} aria-label={label} />;
}
