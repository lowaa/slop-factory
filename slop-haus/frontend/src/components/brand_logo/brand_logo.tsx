import logo32 from "@/assets/logo_32.webp";
import logo64 from "@/assets/logo_64.webp";
import logo128 from "@/assets/logo_128.webp";
import "./brand_logo.scss";

// Decorative: the product name is always rendered as text beside it.
export function BrandLogo({ size }: { size: 32 | 64 }) {
  const [one, two] = size === 32 ? [logo32, logo64] : [logo64, logo128];
  return (
    <img
      className="brand-logo"
      src={one}
      srcSet={`${one} 1x, ${two} 2x`}
      width={size}
      height={size}
      alt=""
    />
  );
}
