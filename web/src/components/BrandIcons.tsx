/**
 * Brand marks the icon set does not carry.
 *
 * lucide ships `Github`, but its `X` is the close glyph and its `Twitter` is the
 * retired bird — neither is the X brand. These two are inline SVG paths rather
 * than remote assets because the dashboard's Content-Security-Policy allows no
 * external origins, and an `<img>` from a CDN would simply fail to load.
 */

interface IconProps {
  size?: number
}

export function DiscordIcon({ size = 16 }: IconProps) {
  return (
    <svg aria-hidden="true" fill="currentColor" height={size} viewBox="0 0 24 24" width={size}>
      <path d="M20.317 4.369A19.79 19.79 0 0 0 15.432 3c-.21.375-.455.88-.624 1.283a18.27 18.27 0 0 0-5.487 0A12.6 12.6 0 0 0 8.69 3a19.74 19.74 0 0 0-4.886 1.372C.72 8.98-.12 13.475.3 17.9a19.9 19.9 0 0 0 6.03 3.06c.487-.664.92-1.37 1.293-2.113a12.9 12.9 0 0 1-2.037-.978c.171-.126.338-.257.5-.392a14.2 14.2 0 0 0 12.028 0c.163.14.33.271.5.392-.65.383-1.334.71-2.04.98.373.74.805 1.447 1.292 2.111a19.87 19.87 0 0 0 6.032-3.06c.492-5.13-.84-9.583-3.52-13.532ZM8.02 15.207c-1.183 0-2.157-1.086-2.157-2.42 0-1.332.951-2.42 2.157-2.42 1.204 0 2.18 1.087 2.157 2.42 0 1.334-.953 2.42-2.157 2.42Zm7.975 0c-1.183 0-2.157-1.086-2.157-2.42 0-1.332.951-2.42 2.157-2.42 1.205 0 2.18 1.087 2.157 2.42 0 1.334-.952 2.42-2.157 2.42Z" />
    </svg>
  )
}

export function XIcon({ size = 16 }: IconProps) {
  return (
    <svg aria-hidden="true" fill="currentColor" height={size} viewBox="0 0 24 24" width={size}>
      <path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231 5.45-6.231Zm-1.161 17.52h1.833L7.084 4.126H5.117l11.966 15.644Z" />
    </svg>
  )
}
