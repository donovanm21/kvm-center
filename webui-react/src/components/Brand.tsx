export function BrandLogo({ size = 40 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" fill="none" aria-label="KVM Center">
      <defs>
        <linearGradient id="brand-logo-accent" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0%" stopColor="#FF8A3C" />
          <stop offset="100%" stopColor="#E3571E" />
        </linearGradient>
      </defs>
      <path
        d="M16 3 L27 10 L27 22 L16 29 L5 22 L5 10 Z"
        fill="none"
        stroke="url(#brand-logo-accent)"
        strokeWidth="2"
        strokeLinejoin="round"
      />
      <rect x="10" y="11" width="12" height="3" rx="0.5" fill="url(#brand-logo-accent)" />
      <rect x="10" y="15" width="9" height="3" rx="0.5" fill="#E3571E" opacity="0.7" />
      <rect x="10" y="19" width="6" height="3" rx="0.5" fill="#E3571E" opacity="0.45" />
    </svg>
  );
}
